package subsonic

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	. "github.com/Masterminds/squirrel"
	"github.com/deluan/sanitize"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/publicurl"
	"github.com/navidrome/navidrome/core/tidal"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
	"github.com/navidrome/navidrome/utils/slice"
	"golang.org/x/sync/errgroup"
)

type searchParams struct {
	query        string
	artistCount  int
	artistOffset int
	albumCount   int
	albumOffset  int
	songCount    int
	songOffset   int
}

func (api *Router) getSearchParams(r *http.Request) (*searchParams, error) {
	p := req.Params(r)
	sp := &searchParams{}
	sp.query = p.StringOr("query", `""`)
	sp.artistCount = p.IntOr("artistCount", 20)
	sp.artistOffset = p.IntOr("artistOffset", 0)
	sp.albumCount = p.IntOr("albumCount", 20)
	sp.albumOffset = p.IntOr("albumOffset", 0)
	sp.songCount = p.IntOr("songCount", 20)
	sp.songOffset = p.IntOr("songOffset", 0)
	return sp, nil
}

type searchFunc[T any] func(q string, options ...model.QueryOptions) (T, error)

func callSearch[T any](ctx context.Context, s searchFunc[T], q string, options model.QueryOptions, result *T) func() error {
	return func() error {
		if options.Max == 0 {
			return nil
		}
		typ := strings.TrimPrefix(reflect.TypeOf(*result).String(), "model.")
		var err error
		start := time.Now()
		*result, err = s(q, options)
		if err != nil {
			log.Error(ctx, "Error searching "+typ, "query", q, "elapsed", time.Since(start), err)
		} else {
			log.Trace(ctx, "Search for "+typ+" completed", "query", q, "elapsed", time.Since(start))
		}
		return nil
	}
}

func (api *Router) searchAll(ctx context.Context, sp *searchParams, musicFolderIds []int) (mediaFiles model.MediaFiles, albums model.Albums, artists model.Artists) {
	start := time.Now()
	q := sanitize.Accents(strings.ToLower(strings.TrimSuffix(sp.query, "*")))

	var tidalLibID int
	if conf.Server.Tidal.Enabled {
		// Resolve the Tidal library ID synchronously — it is needed immediately
		// below to inject the Tidal library into the DB-filter so Subsonic
		// clients that pass only their local musicFolderId still see Tidal results.
		if id, err := tidal.GetOrCreateTidalLibrary(ctx, api.ds); err == nil {
			tidalLibID = id
		}
		// Run the hifi-api JIT sync SYNCHRONOUSLY (bounded by a short timeout) so the
		// freshly-imported Tidal tracks are visible in the SAME search response,
		// rather than only on the next identical search. A 60-second query cache
		// (WasSyncedRecently / MarkSynced) prevents redundant calls — and the extra
		// latency — when the user types the same query repeatedly. The sync uses its
		// own bounded context (not the request's) so a client disconnect mid-search
		// can't cancel it half-way through the DB writes.
		if !tidal.WasSyncedRecently(q) {
			tidal.MarkSynced(q)
			syncLimit := sp.songCount
			if syncLimit < 20 {
				syncLimit = 20
			}
			syncCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			client := tidal.NewClient()
			if err := tidal.SyncSearchWithLibrary(syncCtx, api.ds, client, q, syncLimit, tidalLibID); err != nil {
				log.Warn(ctx, "Tidal JIT sync failed or timed out", "query", q, err)
			}
			cancel()
		}
	}

	// Build options with offset/size/filters packed in
	songOpts := model.QueryOptions{Max: sp.songCount, Offset: sp.songOffset}
	albumOpts := model.QueryOptions{Max: sp.albumCount, Offset: sp.albumOffset}
	artistOpts := model.QueryOptions{Max: sp.artistCount, Offset: sp.artistOffset}

	if len(musicFolderIds) > 0 {
		libIDs := musicFolderIds
		// Always include the Tidal library so Subsonic clients that pass only their
		// local library's ID still see Tidal results alongside local ones.
		if tidalLibID != 0 {
			found := false
			for _, id := range libIDs {
				if id == tidalLibID {
					found = true
					break
				}
			}
			if !found {
				libIDs = append(libIDs, tidalLibID)
			}
		}
		songOpts.Filters = Eq{"library_id": libIDs}
		albumOpts.Filters = Eq{"library_id": libIDs}
		artistOpts.Filters = Eq{"library_artist.library_id": libIDs}
	}

	// Run searches in parallel
	g, ctx := errgroup.WithContext(ctx)
	g.Go(callSearch(ctx, api.ds.MediaFile(ctx).Search, q, songOpts, &mediaFiles))
	g.Go(callSearch(ctx, api.ds.Album(ctx).Search, q, albumOpts, &albums))
	g.Go(callSearch(ctx, api.ds.Artist(ctx).Search, q, artistOpts, &artists))
	err := g.Wait()
	if err == nil {
		log.Debug(ctx, fmt.Sprintf("Search resulted in %d songs, %d albums and %d artists",
			len(mediaFiles), len(albums), len(artists)), "query", sp.query, "elapsedTime", time.Since(start))
	} else {
		log.Warn(ctx, "Search was interrupted", "query", sp.query, "elapsedTime", time.Since(start), err)
	}

	if conf.Server.Tidal.Enabled && len(mediaFiles) > 1 {
		mediaFiles = rerankTidalByPopularity(mediaFiles)
	}

	return mediaFiles, albums, artists
}

// rerankTidalByPopularity preserves the relative order of local tracks
// (BM25 ranking, user-owned files come first) and moves Tidal tracks after
// them sorted by cached Tidal popularity DESC. Returning only what the
// caller passed in keeps pagination counts correct.
//
// Tidal's /search/tracks already returns by popularity, but the FTS5 BM25
// re-rank loses that order; this restores it using the in-memory cache
// populated by SyncSearch, without any DB schema change.
func rerankTidalByPopularity(mfs model.MediaFiles) model.MediaFiles {
	local := mfs[:0:0]
	tidalHits := make(model.MediaFiles, 0, len(mfs))
	for _, mf := range mfs {
		if mf.ExternalSource == tidal.ExternalSourceTidal {
			tidalHits = append(tidalHits, mf)
		} else {
			local = append(local, mf)
		}
	}
	if len(tidalHits) == 0 {
		return mfs
	}
	sort.SliceStable(tidalHits, func(i, j int) bool {
		pi, _ := tidal.LookupPopularity(tidalHits[i].ID)
		pj, _ := tidal.LookupPopularity(tidalHits[j].ID)
		return pi > pj
	})
	out := make(model.MediaFiles, 0, len(mfs))
	out = append(out, local...)
	out = append(out, tidalHits...)
	return out
}

func (api *Router) Search2(r *http.Request) (*responses.Subsonic, error) {
	ctx := r.Context()
	sp, err := api.getSearchParams(r)
	if err != nil {
		return nil, err
	}

	// Get optional library IDs from musicFolderId parameter
	musicFolderIds, err := selectedMusicFolderIds(r, false)
	if err != nil {
		return nil, err
	}
	mfs, als, as := api.searchAll(ctx, sp, musicFolderIds)

	response := newResponse()
	searchResult2 := &responses.SearchResult2{}
	searchResult2.Artist = slice.Map(as, func(artist model.Artist) responses.Artist {
		a := responses.Artist{
			Id:             artist.ID,
			Name:           artist.Name,
			UserRating:     int32(artist.Rating),
			CoverArt:       artist.CoverArtID().String(),
			ArtistImageUrl: publicurl.ImageURL(r, artist.CoverArtID(), 600),
		}
		if artist.Starred {
			a.Starred = artist.StarredAt
		}
		return a
	})
	searchResult2.Album = slice.MapWithArg(als, ctx, childFromAlbum)
	searchResult2.Song = slice.MapWithArg(mfs, ctx, childFromMediaFile)
	response.SearchResult2 = searchResult2
	return response, nil
}

func (api *Router) Search3(r *http.Request) (*responses.Subsonic, error) {
	ctx := r.Context()
	sp, err := api.getSearchParams(r)
	if err != nil {
		return nil, err
	}

	// Get optional library IDs from musicFolderId parameter
	musicFolderIds, err := selectedMusicFolderIds(r, false)
	if err != nil {
		return nil, err
	}
	mfs, als, as := api.searchAll(ctx, sp, musicFolderIds)

	response := newResponse()
	searchResult3 := &responses.SearchResult3{}
	searchResult3.Artist = slice.MapWithArg(as, r, toArtistID3)
	searchResult3.Album = slice.MapWithArg(als, ctx, buildAlbumID3)
	searchResult3.Song = slice.MapWithArg(mfs, ctx, childFromMediaFile)
	response.SearchResult3 = searchResult3
	return response, nil
}
