package nativeapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/tidal"
	"github.com/navidrome/navidrome/log"
)

// jitRecentQueries throttles identical JIT syncs so a single user keystroke that
// fires /song, /album, /artist in parallel doesn't trigger three Tidal searches
// per character. Entries expire after ttl.
type jitThrottle struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

var jitRecent = &jitThrottle{seen: map[string]time.Time{}, ttl: 10 * time.Second}

func (j *jitThrottle) shouldRun(key string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	if t, ok := j.seen[key]; ok && now.Sub(t) < j.ttl {
		return false
	}
	j.seen[key] = now
	// Opportunistic cleanup
	for k, t := range j.seen {
		if now.Sub(t) > j.ttl {
			delete(j.seen, k)
		}
	}
	return true
}

// tidalJITMiddleware intercepts REST list requests (e.g. GET /api/song?_filter=...)
// and syncs matching Tidal results into the DB before the request is served, so
// the default web UI can discover Tidal content on-demand.
func (api *Router) tidalJITMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conf.Server.Tidal.Enabled && r.Method == http.MethodGet {
			if q := extractFilterQuery(r); q != "" {
				if jitRecent.shouldRun(q) {
					ctx := r.Context()
					client := tidal.NewClient()
					if err := tidal.SyncSearch(ctx, api.ds, client, q, 20); err != nil {
						log.Debug(ctx, "REST JIT Tidal sync failed", "query", q, err)
					}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// extractFilterQuery returns the user-typed search text from a REST list request.
// React-admin serializes filters as a JSON object in the `_filter` query param,
// e.g. `?_filter={"title":"radiohead"}` for songs, `{"name":"..."}` for albums/artists.
func extractFilterQuery(r *http.Request) string {
	raw := r.URL.Query().Get("_filter")
	if raw == "" {
		// Direct filter keys (e.g. title=radiohead) used by some clients
		for _, k := range []string{"title", "name"} {
			if v := strings.TrimSpace(r.URL.Query().Get(k)); v != "" {
				return v
			}
		}
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	for _, k := range []string{"title", "name"} {
		if v, ok := parsed[k].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" && v != `""` {
				return v
			}
		}
	}
	return ""
}
