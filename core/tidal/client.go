package tidal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
)

// Client wraps HTTP calls to the hifi-api Tidal proxy.
// httpClient has a bounded timeout for JSON API calls; streamClient has no
// overall timeout because http.Client.Timeout covers body reads too, which
// would kill any stream longer than the timeout while it's being consumed.
type Client struct {
	httpClient   *http.Client
	streamClient *http.Client
	baseURL      string
}

// Shared HTTP clients. We initialize once so every NewClient() call reuses
// the same connection pool — otherwise iOS AVPlayer, which opens many short
// Range requests, would keep dialing fresh TCP+TLS connections to the CDN.
var (
	sharedClientsOnce  sync.Once
	sharedHTTPClient   *http.Client
	sharedStreamClient *http.Client
)

func initSharedClients() {
	// streamTransport bounds connection and response-header time so a dead
	// CDN route surfaces an error instead of hanging past the AVPlayer
	// stall threshold. Body reads aren't covered here (that would cap the
	// whole download); the ranged reader polices those with stallTimeout.
	streamTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		ForceAttemptHTTP2:     true,
	}
	sharedHTTPClient = &http.Client{Timeout: 30 * time.Second}
	sharedStreamClient = &http.Client{Timeout: 0, Transport: streamTransport}
}

func NewClient() *Client {
	sharedClientsOnce.Do(initSharedClients)
	return &Client{
		httpClient:   sharedHTTPClient,
		streamClient: sharedStreamClient,
		baseURL:      conf.Server.Tidal.HifiApiURL,
	}
}

// doGet performs a GET request to hifi-api and decodes the JSON response into `out`.
func (c *Client) doGet(ctx context.Context, path string, params url.Values, out any) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("tidal: invalid base URL %q: %w", c.baseURL, err)
	}
	u.Path = path
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("tidal: creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("tidal: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("tidal: upstream returned %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("tidal: decoding response: %w", err)
	}
	return nil
}

// SearchTracks searches for tracks on Tidal.
func (c *Client) SearchTracks(ctx context.Context, query string, limit, offset int) (*SearchResponse, error) {
	params := url.Values{
		"s":      {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	var resp SearchResponse
	err := c.doGet(ctx, "/search/", params, &resp)
	return &resp, err
}

// SearchAlbums searches for albums on Tidal.
func (c *Client) SearchAlbums(ctx context.Context, query string, limit, offset int) (*SearchResponse, error) {
	params := url.Values{
		"al":     {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	var resp SearchResponse
	err := c.doGet(ctx, "/search/", params, &resp)
	return &resp, err
}

// SearchArtists searches for artists on Tidal.
func (c *Client) SearchArtists(ctx context.Context, query string, limit, offset int) (*SearchResponse, error) {
	params := url.Values{
		"a":      {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	var resp SearchResponse
	err := c.doGet(ctx, "/search/", params, &resp)
	return &resp, err
}

// GetTrackInfo gets detailed info about a track.
func (c *Client) GetTrackInfo(ctx context.Context, id int) (*TrackInfoResponse, error) {
	params := url.Values{"id": {strconv.Itoa(id)}}
	var resp TrackInfoResponse
	err := c.doGet(ctx, "/info/", params, &resp)
	return &resp, err
}

// GetTrackStream gets the playback info (manifest/URL) for a track.
func (c *Client) GetTrackStream(ctx context.Context, id int, quality string) (*TrackStreamResponse, error) {
	if quality == "" {
		quality = conf.Server.Tidal.DefaultQuality
	}
	params := url.Values{
		"id":      {strconv.Itoa(id)},
		"quality": {quality},
	}
	var resp TrackStreamResponse
	err := c.doGet(ctx, "/track/", params, &resp)
	return &resp, err
}

// GetAlbum gets album details including all tracks.
func (c *Client) GetAlbum(ctx context.Context, id int) (*AlbumResponse, error) {
	params := url.Values{"id": {strconv.Itoa(id)}}
	var resp AlbumResponse
	err := c.doGet(ctx, "/album/", params, &resp)
	return &resp, err
}

// GetArtist gets basic artist metadata.
func (c *Client) GetArtist(ctx context.Context, id int) (*ArtistResponse, error) {
	params := url.Values{"id": {strconv.Itoa(id)}}
	var resp ArtistResponse
	err := c.doGet(ctx, "/artist/", params, &resp)
	return &resp, err
}

// GetArtistAlbums gets an artist's albums and tracks.
func (c *Client) GetArtistAlbums(ctx context.Context, id int, skipTracks bool) (*ArtistAlbumsResponse, error) {
	params := url.Values{
		"f":           {strconv.Itoa(id)},
		"skip_tracks": {strconv.FormatBool(skipTracks)},
	}
	var resp ArtistAlbumsResponse
	err := c.doGet(ctx, "/artist/", params, &resp)
	return &resp, err
}

// GetCoverArt gets cover art URLs for a track.
func (c *Client) GetCoverArt(ctx context.Context, id int) (*CoverResponse, error) {
	params := url.Values{"id": {strconv.Itoa(id)}}
	var resp CoverResponse
	err := c.doGet(ctx, "/cover/", params, &resp)
	return &resp, err
}

// GetLyrics gets lyrics for a track.
func (c *Client) GetLyrics(ctx context.Context, id int) (*LyricsResponse, error) {
	params := url.Values{"id": {strconv.Itoa(id)}}
	var resp LyricsResponse
	err := c.doGet(ctx, "/lyrics/", params, &resp)
	return &resp, err
}

// GetRecommendations gets track recommendations based on a track ID.
func (c *Client) GetRecommendations(ctx context.Context, id int) (*RecommendationsResponse, error) {
	params := url.Values{"id": {strconv.Itoa(id)}}
	var resp RecommendationsResponse
	err := c.doGet(ctx, "/recommendations/", params, &resp)
	return &resp, err
}

// StreamAudio fetches the raw audio stream for a Tidal track.
// It returns:
//   - body: the HTTP response body (caller must close)
//   - seeker: non-nil when the stream supports seeking (BTS/direct file). http.ServeContent
//     uses this to serve Range requests, which iOS AVPlayer (Arpeggi) requires.
//   - contentType / size: metadata for response headers (size==0 means unknown, e.g. DASH).
func (c *Client) StreamAudio(ctx context.Context, trackID int, quality string) (io.ReadCloser, io.Seeker, string, int64, error) {
	if quality == "" {
		quality = conf.Server.Tidal.DefaultQuality
	}

	streamResp, err := c.GetTrackStream(ctx, trackID, quality)
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("tidal: getting stream info: %w", err)
	}

	if streamResp.Data.ManifestMimeType == "application/dash+xml" {
		log.Debug(ctx, "Streaming Tidal DASH track", "trackID", trackID, "quality", quality,
			"audioQuality", streamResp.Data.AudioQuality)
		r, ct, err := ExtractDASHStream(ctx, streamResp.Data.Manifest, c.streamClient)
		if err != nil {
			return nil, nil, "", 0, fmt.Errorf("tidal: dash extraction: %w", err)
		}
		// DASH is segmented; size is unknown and seeking isn't supported here.
		return r, nil, ct, 0, nil
	}

	audioURL, contentType, err := streamResp.Data.ExtractAudioURL()
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("tidal: extracting audio URL: %w", err)
	}

	log.Debug(ctx, "Streaming Tidal track", "trackID", trackID, "quality", quality,
		"audioQuality", streamResp.Data.AudioQuality, "contentType", contentType)

	rr, err := newHTTPRangeReader(ctx, c.streamClient, audioURL)
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("tidal: opening ranged reader: %w", err)
	}
	return rr, rr, contentType, rr.Size(), nil
}
