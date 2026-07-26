package tidal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ExternalSourceTidal is the constant used to mark content as coming from Tidal.
const ExternalSourceTidal = "tidal"

// --- Search Response ---

type SearchResponse struct {
	Version string     `json:"version"`
	Data    SearchData `json:"data"`
}

type SearchData struct {
	Limit              int          `json:"limit"`
	Offset             int          `json:"offset"`
	TotalNumberOfItems int          `json:"totalNumberOfItems"`
	Items              []TidalTrack `json:"items"`
	// For artist/album search (top-hits format)
	Artists *SearchArtists `json:"artists,omitempty"`
	Albums  *SearchAlbums  `json:"albums,omitempty"`
}

type SearchArtists struct {
	Limit              int           `json:"limit"`
	Offset             int           `json:"offset"`
	TotalNumberOfItems int           `json:"totalNumberOfItems"`
	Items              []TidalArtist `json:"items"`
}

type SearchAlbums struct {
	Limit              int          `json:"limit"`
	Offset             int          `json:"offset"`
	TotalNumberOfItems int          `json:"totalNumberOfItems"`
	Items              []TidalAlbum `json:"items"`
}

// --- Track ---

type TidalTrack struct {
	ID             int               `json:"id"`
	Title          string            `json:"title"`
	Duration       int               `json:"duration"`
	ReplayGain     float64           `json:"replayGain"`
	Peak           float64           `json:"peak"`
	AllowStreaming bool              `json:"allowStreaming"`
	StreamReady    bool              `json:"streamReady"`
	TrackNumber    int               `json:"trackNumber"`
	VolumeNumber   int               `json:"volumeNumber"`
	Version        string            `json:"version"`
	Popularity     int               `json:"popularity"`
	Copyright      string            `json:"copyright"`
	BPM            int               `json:"bpm"`
	URL            string            `json:"url"`
	ISRC           string            `json:"isrc"`
	Explicit       bool              `json:"explicit"`
	AudioQuality   string            `json:"audioQuality"`
	AudioModes     []string          `json:"audioModes"`
	MediaMetadata  MediaMetadata     `json:"mediaMetadata"`
	Artist         TidalArtistRef    `json:"artist"`
	Artists        []TidalArtistRef  `json:"artists"`
	Album          TidalAlbumRef     `json:"album"`
	Mixes          map[string]string `json:"mixes"`
}

type MediaMetadata struct {
	Tags []string `json:"tags"`
}

type TidalArtistRef struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
	Type    string `json:"type"`
}

type TidalAlbumRef struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	Cover        string `json:"cover"`
	VibrantColor string `json:"vibrantColor"`
}

// --- Album ---

type AlbumResponse struct {
	Version string     `json:"version"`
	Data    TidalAlbum `json:"data"`
}

type TidalAlbum struct {
	ID              int              `json:"id"`
	Title           string           `json:"title"`
	Duration        int              `json:"duration"`
	StreamReady     bool             `json:"streamReady"`
	NumberOfTracks  int              `json:"numberOfTracks"`
	NumberOfVolumes int              `json:"numberOfVolumes"`
	ReleaseDate     string           `json:"releaseDate"`
	Copyright       string           `json:"copyright"`
	Type            string           `json:"type"`
	Version         string           `json:"version"`
	URL             string           `json:"url"`
	Cover           string           `json:"cover"`
	VibrantColor    string           `json:"vibrantColor"`
	Explicit        bool             `json:"explicit"`
	UPC             string           `json:"upc"`
	Popularity      int              `json:"popularity"`
	AudioQuality    string           `json:"audioQuality"`
	AudioModes      []string         `json:"audioModes"`
	MediaMetadata   MediaMetadata    `json:"mediaMetadata"`
	Artist          TidalArtistRef   `json:"artist"`
	Artists         []TidalArtistRef `json:"artists"`
	Items           []AlbumItem      `json:"items"`
}

type AlbumItem struct {
	Item TidalTrack `json:"item"`
	Type string     `json:"type"`
}

// --- Artist ---

type ArtistResponse struct {
	Version string       `json:"version"`
	Artist  TidalArtist  `json:"artist"`
	Cover   *ArtistCover `json:"cover,omitempty"`
}

type TidalArtist struct {
	ID                         int      `json:"id"`
	Name                       string   `json:"name"`
	ArtistTypes                []string `json:"artistTypes"`
	URL                        string   `json:"url"`
	Picture                    string   `json:"picture"`
	Popularity                 int      `json:"popularity"`
	SelectedAlbumCoverFallback string   `json:"selectedAlbumCoverFallback"`
}

type ArtistCover struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	URL  string `json:"750"`
}

type ArtistAlbumsResponse struct {
	Version string       `json:"version"`
	Albums  AlbumPaging  `json:"albums"`
	Tracks  []TidalTrack `json:"tracks"`
}

type AlbumPaging struct {
	Items []TidalAlbum `json:"items"`
}

// --- Track Info ---

type TrackInfoResponse struct {
	Version string     `json:"version"`
	Data    TidalTrack `json:"data"`
}

// --- Track Stream ---

type TrackStreamResponse struct {
	Version string          `json:"version"`
	Data    TrackStreamData `json:"data"`
}

type TrackStreamData struct {
	TrackID            int     `json:"trackId"`
	AssetPresentation  string  `json:"assetPresentation"`
	AudioMode          string  `json:"audioMode"`
	AudioQuality       string  `json:"audioQuality"`
	ManifestMimeType   string  `json:"manifestMimeType"`
	ManifestHash       string  `json:"manifestHash"`
	Manifest           string  `json:"manifest"`
	AlbumReplayGain    float64 `json:"albumReplayGain"`
	AlbumPeakAmplitude float64 `json:"albumPeakAmplitude"`
	TrackReplayGain    float64 `json:"trackReplayGain"`
	TrackPeakAmplitude float64 `json:"trackPeakAmplitude"`
	BitDepth           int     `json:"bitDepth"`
	SampleRate         int     `json:"sampleRate"`
}

// BTSManifest is the decoded manifest for BTS (application/vnd.tidal.bts) format.
type BTSManifest struct {
	MimeType       string   `json:"mimeType"`
	Codecs         string   `json:"codecs"`
	EncryptionType string   `json:"encryptionType"`
	URLs           []string `json:"urls"`
}

// ExtractAudioURL decodes the manifest and returns the direct audio URL and content type.
func (d *TrackStreamData) ExtractAudioURL() (audioURL string, contentType string, err error) {
	if d.ManifestMimeType == "application/vnd.tidal.bts" {
		// BTS format: base64-encoded JSON with direct URLs
		decoded, err := base64.StdEncoding.DecodeString(d.Manifest)
		if err != nil {
			return "", "", fmt.Errorf("decoding BTS manifest: %w", err)
		}
		var manifest BTSManifest
		if err := json.Unmarshal(decoded, &manifest); err != nil {
			return "", "", fmt.Errorf("parsing BTS manifest: %w", err)
		}
		if len(manifest.URLs) == 0 {
			return "", "", fmt.Errorf("BTS manifest contains no URLs")
		}
		return manifest.URLs[0], manifest.MimeType, nil
	}

	if d.ManifestMimeType == "application/dash+xml" {
		// DASH format: typically for Hi-Res, we can't easily stream this directly.
		// For now, return an error suggesting to use a lower quality.
		// In the future we could parse the MPD and fetch segments.
		return "", "", fmt.Errorf("DASH manifests (Hi-Res) are not yet supported for direct streaming; use LOSSLESS quality")
	}

	return "", "", fmt.Errorf("unknown manifest type: %s", d.ManifestMimeType)
}

// --- Cover Art ---

type CoverResponse struct {
	Version string       `json:"version"`
	Covers  []CoverEntry `json:"covers"`
}

type CoverEntry struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	URL1280 string `json:"1280"`
	URL640  string `json:"640"`
	URL80   string `json:"80"`
}

// --- Lyrics ---

type LyricsResponse struct {
	Version string     `json:"version"`
	Lyrics  LyricsData `json:"lyrics"`
}

type LyricsData struct {
	TrackID               int    `json:"trackId"`
	Provider              string `json:"lyricsProvider"`
	ProviderCommonTrackID string `json:"providerCommontrackId"`
	ProviderLyricsID      string `json:"providerLyricsId"`
	Lyrics                string `json:"lyrics"`
	Subtitles             string `json:"subtitles"`
	IsRightToLeft         bool   `json:"isRightToLeft"`
}

// --- Recommendations ---

type RecommendationsResponse struct {
	Version string              `json:"version"`
	Data    RecommendationsData `json:"data"`
}

type RecommendationsData struct {
	Limit              int                  `json:"limit"`
	Offset             int                  `json:"offset"`
	TotalNumberOfItems int                  `json:"totalNumberOfItems"`
	Items              []RecommendationItem `json:"items"`
}

type RecommendationItem struct {
	Track   TidalTrack `json:"track"`
	Sources []string   `json:"sources"`
}

// --- Helper Functions ---

// TidalCoverURL constructs a Tidal cover art URL from a cover UUID and size.
func TidalCoverURL(coverUUID string, size int) string {
	if coverUUID == "" {
		return ""
	}
	slug := strings.ReplaceAll(coverUUID, "-", "/")
	return fmt.Sprintf("https://resources.tidal.com/images/%s/%dx%d.jpg", slug, size, size)
}

// TidalArtistImageURL constructs a Tidal artist image URL from a picture UUID and size.
func TidalArtistImageURL(pictureUUID string, size int) string {
	if pictureUUID == "" {
		return ""
	}
	slug := strings.ReplaceAll(pictureUUID, "-", "/")
	return fmt.Sprintf("https://resources.tidal.com/images/%s/%dx%d.jpg", slug, size, size)
}

// TidalTrackID formats a Tidal track ID for use as a Navidrome external ID.
func TidalTrackID(id int) string {
	return fmt.Sprintf("tidal-track-%d", id)
}

// TidalAlbumID formats a Tidal album ID for use as a Navidrome external ID.
func TidalAlbumID(id int) string {
	return fmt.Sprintf("tidal-album-%d", id)
}

// TidalArtistID formats a Tidal artist ID for use as a Navidrome external ID.
func TidalArtistID(id int) string {
	return fmt.Sprintf("tidal-artist-%d", id)
}
