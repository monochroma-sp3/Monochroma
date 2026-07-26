package tidal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// stallTimeout is how long Read may wait on the Tidal CDN body without
// receiving any bytes before we give up and close the connection.
// iOS AVPlayer (Arpeggi) has its own ~10-15s stall detection; closing
// earlier forces iOS to reissue the Range request sooner, so the user
// perceives a short hiccup instead of a long freeze.
const stallTimeout = 6 * time.Second

// httpRangeReader implements io.ReadSeekCloser on top of an HTTP URL that
// supports Range requests (as the Tidal CDN does). Needed so
// http.ServeContent can satisfy iOS AVPlayer's ranged reads — without seek
// support Arpeggi cancels the connection almost immediately.
type httpRangeReader struct {
	ctx    context.Context
	client *http.Client
	url    string
	size   int64

	mu   sync.Mutex
	pos  int64
	body io.ReadCloser // open body for the current range; nil until first Read after a Seek
}

// newHTTPRangeReader probes the upstream for total size using a `Range: bytes=0-0`
// request (more portable than HEAD across CDNs) and returns a reader positioned at 0.
func newHTTPRangeReader(ctx context.Context, client *http.Client, url string) (*httpRangeReader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("tidal: creating probe request: %w", err)
	}
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tidal: probing upstream: %w", err)
	}
	defer resp.Body.Close()

	// 206 Partial Content → server honors ranges; parse Content-Range for total size.
	// 200 OK → server ignored the Range header; use Content-Length as total size.
	var size int64
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.Header.Get("Content-Range")
		size = parseContentRangeTotal(cr)
		if size <= 0 {
			return nil, fmt.Errorf("tidal: could not parse Content-Range %q", cr)
		}
	case http.StatusOK:
		size = resp.ContentLength
		if size <= 0 {
			return nil, fmt.Errorf("tidal: upstream did not supply size (no Content-Length, no Range support)")
		}
	default:
		return nil, fmt.Errorf("tidal: probe returned status %d", resp.StatusCode)
	}

	return &httpRangeReader{
		ctx:    ctx,
		client: client,
		url:    url,
		size:   size,
	}, nil
}

// parseContentRangeTotal extracts TOTAL from "bytes START-END/TOTAL". Returns 0 on failure.
func parseContentRangeTotal(v string) int64 {
	const prefix = "bytes "
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, prefix) {
		return 0
	}
	rest := v[len(prefix):]
	slash := strings.LastIndex(rest, "/")
	if slash < 0 {
		return 0
	}
	totalStr := rest[slash+1:]
	if totalStr == "*" {
		return 0
	}
	total, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil {
		return 0
	}
	return total
}

func (r *httpRangeReader) Size() int64 { return r.size }

func (r *httpRangeReader) openAt(pos int64) error {
	if pos >= r.size {
		return io.EOF
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	// Open-ended range so the upstream keeps streaming until EOF; we only
	// re-open on Seek, not on every Read. Connect/response-header time is
	// bounded by the Transport's ResponseHeaderTimeout on streamClient;
	// body reads are policed by stallTimeout inside Read.
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", pos))
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("tidal: upstream returned %d for range request", resp.StatusCode)
	}
	r.body = resp.Body
	return nil
}

func (r *httpRangeReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.body == nil {
		if err := r.openAt(r.pos); err != nil {
			return 0, err
		}
	}

	// Run the underlying Read with a stall timeout so a hung CDN connection
	// (flaky WiFi, CDN hiccup) doesn't leave the client buffer starved for
	// the full AVPlayer timeout. On timeout we close the body and return an
	// error — the client will reopen with a new Range request.
	//
	// The goroutine must NOT read into the caller's p: on the timeout and
	// cancel paths this Read returns while the goroutine may still be blocked
	// inside body.Read, and http.ServeContent hands the same buffer straight
	// back to the next Read. An orphaned goroutine would then scribble over a
	// buffer that a live Read is already using. It reads into its own buffer
	// instead, and we copy into p only on the success path.
	type readResult struct {
		buf []byte
		n   int
		err error
	}
	// body is captured before the goroutine starts so that the goroutine never
	// touches r.body concurrently with the timeout/cancel paths clearing it.
	body := r.body
	buf := make([]byte, len(p))
	ch := make(chan readResult, 1)
	go func() {
		n, err := body.Read(buf)
		ch <- readResult{buf, n, err}
	}()

	// An explicit timer (stopped on every return path) instead of time.After:
	// ServeContent issues thousands of Reads per track, and time.After leaves
	// each timer alive until it fires.
	timer := time.NewTimer(stallTimeout)
	defer timer.Stop()

	select {
	case res := <-ch:
		n := copy(p, res.buf[:res.n])
		r.pos += int64(n)
		if res.err == io.EOF {
			r.body.Close()
			r.body = nil
		}
		return n, res.err
	case <-timer.C:
		// The in-flight goroutine will unblock once the TCP connection is
		// torn down by Close; its result (and its private buffer) is discarded.
		r.body.Close()
		r.body = nil
		return 0, fmt.Errorf("tidal: upstream stalled for %s at pos=%d", stallTimeout, r.pos)
	case <-r.ctx.Done():
		r.body.Close()
		r.body = nil
		return 0, r.ctx.Err()
	}
}

func (r *httpRangeReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = r.pos + offset
	case io.SeekEnd:
		newPos = r.size + offset
	default:
		return 0, errors.New("tidal: invalid whence")
	}
	if newPos < 0 {
		return 0, errors.New("tidal: negative seek position")
	}
	if newPos != r.pos {
		if r.body != nil {
			r.body.Close()
			r.body = nil
		}
		r.pos = newPos
	}
	return r.pos, nil
}

func (r *httpRangeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.body != nil {
		err := r.body.Close()
		r.body = nil
		return err
	}
	return nil
}
