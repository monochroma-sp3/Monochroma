package tidal

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var (
	initRegex  = regexp.MustCompile(`initialization="([^"]+)"`)
	mediaRegex = regexp.MustCompile(`media="([^"]+)"`)
	// sTagRegex grabs whole <S .../> tags; attributes are then parsed
	// individually by attrRegex so their order in the tag doesn't matter.
	sTagRegex        = regexp.MustCompile(`<S\s[^>]*>`)
	attrRegex        = regexp.MustCompile(`([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*"([^"]*)"`)
	timescaleRegex   = regexp.MustCompile(`timescale="(\d+)"`)
	mpdDurationRegex = regexp.MustCompile(`mediaPresentationDuration="([^"]+)"`)
	iso8601DurRegex  = regexp.MustCompile(`^P(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)
)

// ExtractDASHStream takes a base64 encoded DASH manifest and returns a sequential reader.
func ExtractDASHStream(ctx context.Context, manifestB64 string, httpClient *http.Client) (io.ReadCloser, string, error) {
	manifest, err := base64.StdEncoding.DecodeString(manifestB64)
	if err != nil {
		return nil, "", fmt.Errorf("invalid base64 manifest: %w", err)
	}
	xmlData := string(manifest)

	mInit := initRegex.FindStringSubmatch(xmlData)
	if len(mInit) < 2 {
		return nil, "", fmt.Errorf("no initialization segment in DASH manifest")
	}

	mMedia := mediaRegex.FindStringSubmatch(xmlData)
	if len(mMedia) < 2 {
		return nil, "", fmt.Errorf("no media segment template in DASH manifest")
	}

	initURL := strings.ReplaceAll(mInit[1], "&amp;", "&")
	mediaURL := strings.ReplaceAll(mMedia[1], "&amp;", "&")

	// Calculate max segments
	maxSeg := countDASHSegments(xmlData)

	if maxSeg == 0 {
		return nil, "", fmt.Errorf("no segments found in DASH timeline")
	}

	return &dashReader{
		ctx:        ctx,
		client:     httpClient,
		initURL:    initURL,
		mediaURL:   mediaURL,
		currentSeg: 0,
		maxSeg:     maxSeg,
	}, "audio/mp4", nil
}

// countDASHSegments walks the manifest's SegmentTimeline and returns the number
// of media segments it describes.
//
// Attribute order inside <S/> is not significant in XML, and DASH manifests do
// commonly emit `<S t="0" d="..." r="..."/>`. Requiring d= to come first (as an
// earlier fixed-order regex did) silently matched only part of the timeline, so
// playback stopped early.
func countDASHSegments(xmlData string) int {
	timescale := int64(1)
	if m := timescaleRegex.FindStringSubmatch(xmlData); len(m) > 1 {
		if ts, err := strconv.ParseInt(m[1], 10, 64); err == nil && ts > 0 {
			timescale = ts
		}
	}
	// Timeline length in timescale ticks, needed to expand r="-1".
	var totalTicks int64
	if m := mpdDurationRegex.FindStringSubmatch(xmlData); len(m) > 1 {
		if secs := parseISO8601Duration(m[1]); secs > 0 {
			totalTicks = int64(secs * float64(timescale))
		}
	}

	var (
		total  int
		cursor int64 // position on the timeline, in ticks
	)
	for _, tag := range sTagRegex.FindAllString(xmlData, -1) {
		attrs := parseXMLAttrs(tag)
		// d (segment duration) is mandatory on <S>; without it the tag
		// describes nothing we can count.
		d, err := strconv.ParseInt(attrs["d"], 10, 64)
		if err != nil || d <= 0 {
			continue
		}
		if t, err := strconv.ParseInt(attrs["t"], 10, 64); err == nil && t >= 0 {
			cursor = t
		}

		count := int64(1)
		if r, err := strconv.ParseInt(attrs["r"], 10, 64); err == nil {
			switch {
			case r >= 0:
				count = r + 1
			case totalTicks > cursor:
				// A negative r means "repeat until the end of the period"
				// (DASH spec), not "repeat r times" — taking it literally
				// subtracted from the segment count. Round up: the last
				// segment may be shorter than d.
				count = (totalTicks - cursor + d - 1) / d
			default:
				// r="-1" with no usable period duration: count the one
				// segment we know about rather than guessing.
				count = 1
			}
		}
		total += int(count)
		cursor += count * d
	}
	return total
}

// parseXMLAttrs pulls key="value" pairs out of a single XML tag.
func parseXMLAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range attrRegex.FindAllStringSubmatch(tag, -1) {
		attrs[m[1]] = m[2]
	}
	return attrs
}

// parseISO8601Duration parses the subset of ISO-8601 durations MPDs use for
// mediaPresentationDuration (e.g. "PT3M14.5S"), returning seconds. 0 on failure.
func parseISO8601Duration(v string) float64 {
	m := iso8601DurRegex.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0
	}
	var secs float64
	for i, mult := range []float64{86400, 3600, 60, 1} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.ParseFloat(m[i+1], 64)
		if err != nil {
			return 0
		}
		secs += n * mult
	}
	return secs
}

type dashReader struct {
	ctx        context.Context
	client     *http.Client
	initURL    string
	mediaURL   string
	currentSeg int
	maxSeg     int
	currReader io.ReadCloser
}

func (d *dashReader) Read(p []byte) (n int, err error) {
	for {
		if d.ctx.Err() != nil {
			return 0, d.ctx.Err()
		}

		if d.currReader == nil {
			var url string
			if d.currentSeg == 0 {
				url = d.initURL
			} else if d.currentSeg <= d.maxSeg {
				url = strings.ReplaceAll(d.mediaURL, "$Number$", strconv.Itoa(d.currentSeg))
			} else {
				return 0, io.EOF
			}

			//log.Trace(d.ctx, "Fetching DASH segment", "segment", d.currentSeg, "max", d.maxSeg)
			req, err := http.NewRequestWithContext(d.ctx, "GET", url, nil)
			if err != nil {
				return 0, err
			}

			resp, err := d.client.Do(req)
			if err != nil {
				return 0, err
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				// A definitive "not there" past the first segment means we
				// overshot the timeline computed from the manifest — a clean
				// end of stream.
				if d.currentSeg > 0 && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
					return 0, io.EOF
				}
				// Everything else (503, 429, an expired signed URL, ...) is a
				// real failure. Reporting it as EOF truncated the track
				// silently: the client saw a short but "successful" file.
				return 0, fmt.Errorf("tidal: fetching DASH segment %d: status %d", d.currentSeg, resp.StatusCode)
			}

			d.currReader = resp.Body
		}

		n, err = d.currReader.Read(p)
		if err == io.EOF {
			d.currReader.Close()
			d.currReader = nil
			d.currentSeg++
			if n > 0 {
				return n, nil
			}
			continue // Read from next segment
		}
		return n, err
	}
}

func (d *dashReader) Close() error {
	if d.currReader != nil {
		return d.currReader.Close()
	}
	return nil
}
