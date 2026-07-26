package tidal

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils/str"
)

// TrackToMediaFile converts a TidalTrack to a Navidrome MediaFile.
// The libraryID is the ID of the virtual Tidal library in the DB.
func TrackToMediaFile(t TidalTrack, albumID string, libraryID int) model.MediaFile {
	mf := model.MediaFile{
		ID:            TidalTrackID(t.ID),
		LibraryID:     libraryID,
		FolderID:      TidalFolderID(libraryID),
		Path:          fmt.Sprintf("tidal://track/%d", t.ID),
		Title:         t.Title,
		Album:         t.Album.Title,
		AlbumID:       albumID,
		Artist:        t.Artist.Name,
		ArtistID:      TidalArtistID(t.Artist.ID),
		AlbumArtist:   t.Artist.Name,
		AlbumArtistID: TidalArtistID(t.Artist.ID),
		TrackNumber:   t.TrackNumber,
		DiscNumber:    t.VolumeNumber,
		Year:          0, // populated later when the full album is synced (TidalAlbumRef has no release date)
		Duration:      float32(t.Duration),
		BitRate:       qualityToBitRate(t.AudioQuality),
		Suffix:        qualityToSuffix(t.AudioQuality),
		Genre:         "",
		Compilation:   false,
		Comment:       t.Copyright,
		BPM:           t.BPM,
		HasCoverArt:   t.Album.Cover != "",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// Set ISRC as MbzRecordingID for matching
	if t.ISRC != "" {
		mf.MbzRecordingID = t.ISRC
	}

	// Set explicit status
	if t.Explicit {
		mf.ExplicitStatus = "e"
	}

	// Set codec based on quality
	mf.Codec = qualityToCodec(t.AudioQuality)

	// Handle multiple artists.
	//
	// Every participant is a row in media_file_artists, which has a FOREIGN KEY
	// onto artist(id) — so a ref without a Tidal ID must never become one. The
	// ID != 0 guard is applied uniformly here, and sync.go ensures the whole
	// credited list (not just the primary artist) before the media_file is put.
	mf.Participants = make(model.Participants)

	if len(t.Artists) > 0 {
		names := make([]string, 0, len(t.Artists))
		for _, a := range t.Artists {
			names = append(names, a.Name)
			if a.ID != 0 {
				mf.Participants.Add(model.RoleArtist, ArtistRefToModel(a))
			}
		}
		mf.Artist = strings.Join(names, ", ")

		// Tidal's artists[0] is the MAIN credit and normally matches `artist`.
		// Only follow it when it carries a real ID; otherwise keep ArtistID on
		// t.Artist, which sync.go always ensures.
		if t.Artists[0].ID != 0 {
			mf.ArtistID = TidalArtistID(t.Artists[0].ID)
		}
	} else if t.Artist.ID != 0 {
		mf.Participants.Add(model.RoleArtist, ArtistRefToModel(t.Artist))
	}

	if t.Artist.ID != 0 {
		mf.Participants.Add(model.RoleAlbumArtist, ArtistRefToModel(t.Artist))
	}

	// Set replay gain
	if t.ReplayGain != 0 {
		rg := t.ReplayGain
		mf.RGTrackGain = &rg
	}
	if t.Peak != 0 {
		pk := t.Peak
		mf.RGTrackPeak = &pk
	}

	// OrderArtistName is required by the matcher for getSimilarSongs2 queries.
	// Use the primary (first) artist, mirroring model/metadata/map_mediafile.go logic.
	if p := mf.Participants.First(model.RoleArtist); p.ID != "" {
		mf.OrderArtistName = str.SanitizeFieldForSortingNoArticle(p.Name)
	} else {
		mf.OrderArtistName = str.SanitizeFieldForSortingNoArticle(mf.Artist)
	}

	return mf
}

// AlbumToModel converts a TidalAlbum to a Navidrome Album.
func AlbumToModel(a TidalAlbum, libraryID int) model.Album {
	album := model.Album{
		ID:            TidalAlbumID(a.ID),
		LibraryID:     libraryID,
		Name:          a.Title,
		AlbumArtist:   a.Artist.Name,
		AlbumArtistID: TidalArtistID(a.Artist.ID),
		SongCount:     a.NumberOfTracks,
		Duration:      float32(a.Duration),
		Size:          0, // Unknown for Tidal
		CatalogNum:    a.UPC,
		Comment:       a.Copyright,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	// Parse release date
	if a.ReleaseDate != "" {
		parts := strings.Split(a.ReleaseDate, "-")
		if len(parts) >= 1 {
			if y, err := strconv.Atoi(parts[0]); err == nil {
				album.MinYear = y
				album.MaxYear = y
				album.Date = a.ReleaseDate
				album.ReleaseDate = a.ReleaseDate
			}
		}
	}

	// Calculate Cover URLs
	if a.Cover != "" {
		album.LargeImageUrl = fmt.Sprintf("https://resources.tidal.com/images/%s/1280x1280.jpg", strings.ReplaceAll(a.Cover, "-", "/"))
		album.MediumImageUrl = fmt.Sprintf("https://resources.tidal.com/images/%s/640x640.jpg", strings.ReplaceAll(a.Cover, "-", "/"))
		album.SmallImageUrl = fmt.Sprintf("https://resources.tidal.com/images/%s/320x320.jpg", strings.ReplaceAll(a.Cover, "-", "/"))
	}

	// Set explicit status
	if a.Explicit {
		album.ExplicitStatus = "e"
	}

	// Handle multiple artists. Same FK constraint as tracks: album_artists
	// references artist(id), so skip refs without a Tidal ID.
	album.Participants = make(model.Participants)

	if len(a.Artists) > 1 {
		names := make([]string, 0, len(a.Artists))
		for _, art := range a.Artists {
			names = append(names, art.Name)
			if art.ID != 0 {
				album.Participants.Add(model.RoleAlbumArtist, ArtistRefToModel(art))
			}
		}
		album.AlbumArtist = strings.Join(names, ", ")
	} else if a.Artist.ID != 0 {
		album.Participants.Add(model.RoleAlbumArtist, ArtistRefToModel(a.Artist))
	}

	return album
}

// ArtistRefToModel converts a TidalArtistRef (from track/album context) to a Navidrome Artist.
func ArtistRefToModel(a TidalArtistRef) model.Artist {
	return model.Artist{
		ID:   TidalArtistID(a.ID),
		Name: a.Name,
	}
}

// --- Helper functions ---

func qualityToBitRate(quality string) int {
	switch quality {
	case "HI_RES_LOSSLESS":
		return 2000 // Approximate, varies by track
	case "LOSSLESS":
		return 1411 // CD quality (16-bit/44.1kHz FLAC)
	case "HIGH":
		return 320
	case "LOW":
		return 96
	default:
		return 1411
	}
}

func qualityToSuffix(quality string) string {
	switch quality {
	case "HI_RES_LOSSLESS", "LOSSLESS":
		return "flac"
	case "HIGH":
		return "mp4" // AAC
	case "LOW":
		return "mp4" // AAC
	default:
		return "flac"
	}
}

func qualityToCodec(quality string) string {
	switch quality {
	case "HI_RES_LOSSLESS", "LOSSLESS":
		return "flac"
	case "HIGH", "LOW":
		return "aac"
	default:
		return "flac"
	}
}

func ExtractYearFromDate(dateStr string) int {
	if dateStr == "" {
		return 0
	}
	parts := strings.Split(dateStr, "-")
	if len(parts) >= 1 {
		y, _ := strconv.Atoi(parts[0])
		return y
	}
	return 0
}
