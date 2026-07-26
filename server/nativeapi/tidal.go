package nativeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/tidal"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// readSeekerAdapter lets us hand an (io.ReadCloser, io.Seeker) pair to
// http.ServeContent, which expects a single io.ReadSeeker value. Close is
// handled by the calling handler via defer body.Close().
type readSeekerAdapter struct {
	io.Reader
	io.Seeker
}

// addTidalRoutes registers the Tidal API routes if Tidal is enabled.
func (api *Router) addTidalRoutes(r chi.Router) {
	if !conf.Server.Tidal.Enabled {
		return
	}
	tidalClient := tidal.NewClient()

	r.Route("/tidal", func(r chi.Router) {
		// Read-only routes: available to any authenticated user. Search/info
		// only proxy Tidal, and stream/cover are needed for normal playback.
		r.Get("/search", tidalSearch(tidalClient))
		r.Get("/track/{id}", tidalTrackInfo(tidalClient))
		r.Get("/album/{id}", tidalAlbumInfo(tidalClient))
		r.Get("/artist/{id}", tidalArtistInfo(tidalClient))

		r.Get("/stream/{id}", tidalStreamProxy(tidalClient))
		r.Get("/cover/{id}", tidalCoverProxy(tidalClient))

		// Mutating routes: these write to (or delete from) the shared
		// `tidal://virtual` library that every user on the instance sees,
		// so they must be restricted to admins.
		r.Group(func(r chi.Router) {
			r.Use(adminOnlyMiddleware)
			r.Post("/import/track/{id}", tidalImportTrack(tidalClient, api.ds))
			r.Post("/import/album/{id}", tidalImportAlbum(tidalClient, api.ds))
			r.Delete("/remove/track/{id}", tidalRemoveTrack(api.ds))
			r.Delete("/remove/album/{id}", tidalRemoveAlbum(api.ds))
		})
	})
}

// tidalSearch handles search requests to Tidal.
func tidalSearch(client *tidal.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		query := r.URL.Query().Get("q")
		searchType := r.URL.Query().Get("type") // "track", "album", "artist"
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

		if limit == 0 {
			limit = 25
		}
		if query == "" {
			http.Error(w, `{"error": "query parameter 'q' is required"}`, http.StatusBadRequest)
			return
		}

		var result any
		var err error

		switch searchType {
		case "album":
			result, err = client.SearchAlbums(ctx, query, limit, offset)
		case "artist":
			result, err = client.SearchArtists(ctx, query, limit, offset)
		default:
			result, err = client.SearchTracks(ctx, query, limit, offset)
		}

		if err != nil {
			log.Error(ctx, "Tidal search failed", "query", query, "type", searchType, err)
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// tidalTrackInfo returns info about a Tidal track.
func tidalTrackInfo(client *tidal.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid track id"}`, http.StatusBadRequest)
			return
		}

		result, err := client.GetTrackInfo(ctx, id)
		if err != nil {
			log.Error(ctx, "Tidal track info failed", "id", id, err)
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// tidalAlbumInfo returns info about a Tidal album.
func tidalAlbumInfo(client *tidal.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid album id"}`, http.StatusBadRequest)
			return
		}

		result, err := client.GetAlbum(ctx, id)
		if err != nil {
			log.Error(ctx, "Tidal album info failed", "id", id, err)
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// tidalArtistInfo returns info about a Tidal artist.
func tidalArtistInfo(client *tidal.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid artist id"}`, http.StatusBadRequest)
			return
		}

		result, err := client.GetArtist(ctx, id)
		if err != nil {
			log.Error(ctx, "Tidal artist info failed", "id", id, err)
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// tidalImportTrack imports a single Tidal track into the Navidrome database.
func tidalImportTrack(client *tidal.Client, ds model.DataStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tidalID, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid track id"}`, http.StatusBadRequest)
			return
		}

		// Get track info from Tidal
		trackResp, err := client.GetTrackInfo(ctx, tidalID)
		if err != nil {
			log.Error(ctx, "Failed to get Tidal track info", "tidalID", tidalID, err)
			http.Error(w, fmt.Sprintf(`{"error": "failed to fetch track: %s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		// Get or create the Tidal library
		libraryID, err := tidal.GetOrCreateTidalLibrary(ctx, ds)
		if err != nil {
			log.Error(ctx, "Failed to get/create Tidal library", err)
			http.Error(w, `{"error": "failed to setup Tidal library"}`, http.StatusInternalServerError)
			return
		}

		track := trackResp.Data

		// Ensure artist exists
		if err := tidal.EnsureTidalArtist(ctx, ds, track.Artist, libraryID); err != nil {
			log.Error(ctx, "Failed to ensure artist", "artist", track.Artist.Name, err)
		}

		// Ensure album exists
		albumID := tidal.TidalAlbumID(track.Album.ID)
		if err := tidal.EnsureTidalAlbumFromRef(ctx, ds, track.Album, track.Artist, libraryID); err != nil {
			log.Error(ctx, "Failed to ensure album", "album", track.Album.Title, err)
		}

		// Create MediaFile
		mf := tidal.TrackToMediaFile(track, albumID, libraryID)
		mf.ExternalSource = tidal.ExternalSourceTidal
		mf.ExternalID = strconv.Itoa(tidalID)

		err = ds.MediaFile(ctx).Put(&mf)
		if err != nil {
			log.Error(ctx, "Failed to save Tidal track", "tidalID", tidalID, err)
			http.Error(w, fmt.Sprintf(`{"error": "failed to save track: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		log.Info(ctx, "Imported Tidal track", "tidalID", tidalID, "title", track.Title, "artist", track.Artist.Name)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"trackId": mf.ID,
			"title":   track.Title,
			"artist":  track.Artist.Name,
		})
	}
}

// tidalImportAlbum imports an entire Tidal album into the Navidrome database.
func tidalImportAlbum(client *tidal.Client, ds model.DataStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tidalID, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid album id"}`, http.StatusBadRequest)
			return
		}

		// Get album info from Tidal
		albumResp, err := client.GetAlbum(ctx, tidalID)
		if err != nil {
			log.Error(ctx, "Failed to get Tidal album info", "tidalID", tidalID, err)
			http.Error(w, fmt.Sprintf(`{"error": "failed to fetch album: %s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		// Get or create the Tidal library
		libraryID, err := tidal.GetOrCreateTidalLibrary(ctx, ds)
		if err != nil {
			log.Error(ctx, "Failed to get/create Tidal library", err)
			http.Error(w, `{"error": "failed to setup Tidal library"}`, http.StatusInternalServerError)
			return
		}

		album := albumResp.Data

		// Ensure all artists exist
		for _, artist := range album.Artists {
			if err := tidal.EnsureTidalArtist(ctx, ds, artist, libraryID); err != nil {
				log.Error(ctx, "Failed to ensure artist", "artist", artist.Name, err)
			}
		}

		// Create Album
		albumModel := tidal.AlbumToModel(album, libraryID)
		albumModel.ExternalSource = tidal.ExternalSourceTidal
		albumModel.ExternalID = strconv.Itoa(tidalID)

		// Set cover art URLs
		if album.Cover != "" {
			albumModel.SmallImageUrl = tidal.TidalCoverURL(album.Cover, 80)
			albumModel.MediumImageUrl = tidal.TidalCoverURL(album.Cover, 640)
			albumModel.LargeImageUrl = tidal.TidalCoverURL(album.Cover, 1280)
		}

		err = ds.Album(ctx).Put(&albumModel)
		if err != nil {
			log.Error(ctx, "Failed to save Tidal album", "tidalID", tidalID, err)
			http.Error(w, fmt.Sprintf(`{"error": "failed to save album: %s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		// Import all tracks
		imported := 0
		for _, item := range album.Items {
			if item.Type != "track" {
				continue
			}
			track := item.Item

			// Set album info on track if missing
			if track.Album.ID == 0 {
				track.Album = tidal.TidalAlbumRef{
					ID:    album.ID,
					Title: album.Title,
					Cover: album.Cover,
				}
			}

			mf := tidal.TrackToMediaFile(track, albumModel.ID, libraryID)
			mf.ExternalSource = tidal.ExternalSourceTidal
			mf.ExternalID = strconv.Itoa(track.ID)

			// Set year from album
			if album.ReleaseDate != "" {
				mf.Year = tidal.ExtractYearFromDate(album.ReleaseDate)
				mf.ReleaseDate = album.ReleaseDate
			}

			if err := ds.MediaFile(ctx).Put(&mf); err != nil {
				log.Error(ctx, "Failed to save Tidal track", "trackID", track.ID, err)
				continue
			}
			imported++
		}

		log.Info(ctx, "Imported Tidal album", "tidalID", tidalID, "title", album.Title,
			"artist", album.Artist.Name, "tracks", imported)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"albumId":        albumModel.ID,
			"title":          album.Title,
			"artist":         album.Artist.Name,
			"tracksImported": imported,
		})
	}
}

// tidalRemoveTrack removes an imported Tidal track from the database.
func tidalRemoveTrack(ds model.DataStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid track id"}`, http.StatusBadRequest)
			return
		}

		mediaFileID := tidal.TidalTrackID(id)
		err = ds.MediaFile(ctx).Delete(mediaFileID)
		if err != nil {
			log.Error(ctx, "Failed to remove Tidal track", "id", id, err)
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// tidalRemoveAlbum removes an imported Tidal album and all its tracks from the database.
func tidalRemoveAlbum(ds model.DataStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid album id"}`, http.StatusBadRequest)
			return
		}

		albumID := tidal.TidalAlbumID(id)

		// Delete all tracks in this album
		tracks, err := ds.MediaFile(ctx).GetAll(model.QueryOptions{
			Filters: squirrel.Eq{"album_id": albumID},
		})
		if err != nil {
			log.Error(ctx, "Failed to find tracks for Tidal album", "albumID", albumID, err)
		} else {
			for _, t := range tracks {
				_ = ds.MediaFile(ctx).Delete(t.ID)
			}
		}

		// Note: Album deletion would require additional methods on the repository.
		// For now, we just delete the tracks. The album will be cleaned up by GC.

		log.Info(ctx, "Removed Tidal album tracks", "albumID", albumID, "tracksRemoved", len(tracks))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "ok",
			"tracksRemoved": len(tracks),
		})
	}
}

// tidalStreamProxy proxies audio streaming from Tidal through Navidrome.
func tidalStreamProxy(client *tidal.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid track id"}`, http.StatusBadRequest)
			return
		}

		quality := r.URL.Query().Get("quality")
		body, seeker, contentType, contentLength, err := client.StreamAudio(ctx, id, quality)
		if err != nil {
			log.Error(ctx, "Tidal stream proxy failed", "trackID", id, err)
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadGateway)
			return
		}
		defer body.Close()

		w.Header().Set("Content-Type", contentType)

		// When the upstream format supports seeking (BTS direct file), expose
		// Range handling through http.ServeContent so browsers and iOS clients
		// can scrub. DASH falls back to plain progressive piping.
		if seeker != nil {
			http.ServeContent(w, r, "", time.Time{}, readSeekerAdapter{body, seeker})
			return
		}

		if contentLength > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		}
		w.Header().Set("Accept-Ranges", "none")
		_, _ = io.Copy(w, body)
	}
}

// tidalCoverProxy proxies cover art from Tidal CDN.
func tidalCoverProxy(client *tidal.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, `{"error": "invalid track id"}`, http.StatusBadRequest)
			return
		}

		coverResp, err := client.GetCoverArt(ctx, id)
		if err != nil || len(coverResp.Covers) == 0 {
			http.Error(w, `{"error": "cover not found"}`, http.StatusNotFound)
			return
		}

		// Redirect to the Tidal CDN
		cover := coverResp.Covers[0]
		size := r.URL.Query().Get("size")
		var url string
		switch size {
		case "small", "80":
			url = cover.URL80
		case "medium", "640":
			url = cover.URL640
		default:
			url = cover.URL1280
		}

		if url == "" {
			url = cover.URL640
		}

		http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	}
}
