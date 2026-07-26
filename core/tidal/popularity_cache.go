package tidal

import (
	"sync"
	"time"
)

// popularityTTL is how long a popularity entry stays valid in the cache.
// Long enough to cover a user's browse session (they search, then scroll
// through results), short enough that stale popularity eventually drops out.
const popularityTTL = 10 * time.Minute

// popularityEntry holds a popularity score plus its expiry. The score is
// Tidal's 0-100 popularity value, copied verbatim from the track payload.
type popularityEntry struct {
	score     int
	expiresAt time.Time
}

var (
	popularityMu    sync.RWMutex
	popularityStore = map[string]popularityEntry{}
	// lastSweep is advisory: we sweep expired entries at most once per TTL
	// interval to keep the map from growing unbounded without paying the
	// cost on every write.
	popularityLastSweep time.Time
)

// RememberPopularity stores the given score for a Navidrome media_file ID
// (the stable tidal-track-<id> form returned by TidalTrackID) so a later
// search can rerank results by popularity without a schema change.
func RememberPopularity(mediaFileID string, score int) {
	if mediaFileID == "" {
		return
	}
	now := time.Now()
	popularityMu.Lock()
	popularityStore[mediaFileID] = popularityEntry{
		score:     score,
		expiresAt: now.Add(popularityTTL),
	}
	if now.Sub(popularityLastSweep) > popularityTTL {
		for id, entry := range popularityStore {
			if now.After(entry.expiresAt) {
				delete(popularityStore, id)
			}
		}
		popularityLastSweep = now
	}
	popularityMu.Unlock()
}

// LookupPopularity returns the cached Tidal popularity for a Navidrome
// media_file ID, or (0, false) if the entry is missing or expired.
func LookupPopularity(mediaFileID string) (int, bool) {
	popularityMu.RLock()
	entry, ok := popularityStore[mediaFileID]
	popularityMu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return 0, false
	}
	return entry.score, true
}
