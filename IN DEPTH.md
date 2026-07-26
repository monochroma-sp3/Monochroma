# Tidal Integration: In Depth


- [How the Tidal Integration Works](#how-the-tidal-integration-works)
- [Technical Architecture](#technical-architecture)
  - [Tidal module](#tidal-module-coretidal)
  - [How streaming works](#how-streaming-works)
  - [Virtual library model](#virtual-library-model)
  - [Backend overview](#backend-go)
  - [Frontend overview](#frontend-react--typescript)
  - [Database](#database)
  - [API endpoints](#api-endpoints)

---

## How the Tidal Integration Works

Tidal content lives in a **virtual library** (`tidal://virtual`) inside the same SQLite database as your local tracks. Everything is baked into the same binary.

The integration is **Just-In-Time (JIT)**: content is fetched from Tidal and synced into the local database on demand, so any Subsonic client or the built-in web UI discovers Tidal results automatically when you search.
This is also the reason why the catalogue looks empty at first: it grows as you search more and more songs.

```
User searches "Radiohead"
       │
       ▼
JIT Middleware (REST) ──► Tidal hifi-api ──► Parallel search: tracks + albums + artists
or Subsonic search                               │
       │                                         ▼
       │                             Upsert into local SQLite DB
       │                              (artist / album / media_file rows,
       │                               tagged with external_source="tidal")
       ▼
Normal DB query returns local + Tidal results together
Tidal tracks ranked by popularity score, local tracks ranked by BM25 relevance
```

When a Tidal track is played:

```
Media streamer sees ExternalSource="tidal"
       │
       ▼
client.StreamAudio() ──► GET /track/ on hifi-api
       │
       ├─ BTS manifest (LOSSLESS / HI_RES) ──► ranged_reader over CDN URL
       │                                         HTTP Range requests supported
       │                                         → iOS AVPlayer / scrubbing works
       │
       └─ DASH manifest (HI_RES_LOSSLESS) ──► DASH segment fetcher
                                               Sequential segments, no seeking
```

---
## Technical Architecture

### Tidal module (`core/tidal/`)

| File | Responsibility |
|------|---------------|
| `client.go` | HTTP client for hifi-api. Two pooled clients: `httpClient` (30 s timeout, JSON calls) and `streamClient` (no overall timeout, bounded dial/TLS/header timeouts). Shared via `sync.Once` so iOS AVPlayer's many short Range requests reuse TCP connections. |
| `models.go` | All Tidal data types: `TidalTrack`, `TidalAlbum`, `TidalArtist`, manifest structs (BTS / DASH). Helper functions: `TidalCoverURL`, `TidalArtistImageURL`, `TidalTrackID`, `TidalAlbumID`, `TidalArtistID`. |
| `converter.go` | `TrackToMediaFile`, `AlbumToModel`, `ArtistRefToModel`. Maps quality → bitrate/codec/suffix. Extracts replay gain, explicit flag, multi-artist credits. |
| `sync.go` | JIT sync engine. `SyncSearch` fires three parallel searches (tracks + albums + artists) via `sync.WaitGroup`. Upserts results into `media_file` / `album` / `artist`. `GetOrCreateTidalLibrary` manages the virtual library row. 60 s dedup cache prevents hammering hifi-api on repeat queries. |
| `dash.go` | DASH MPD parser. Base64-decodes the manifest XML, extracts initialization segment and media template, streams segments sequentially as an `io.Reader`. |
| `ranged_reader.go` | `io.ReadSeekCloser` over HTTP Range requests. Probes upstream size with `Range: bytes=0-0`. 6 s stall timeout for hung CDN connections. Enables `http.ServeContent` Range handling for seekable formats. |
| `popularity_cache.go` | 10-minute in-memory TTL cache (swept periodically, not size-bounded) for Tidal popularity scores (0–100). Used by search results to rerank Tidal tracks by popularity without a DB schema change. |

### How streaming works

When the media streamer sees `ExternalSource = "tidal"`:

1. Calls `client.StreamAudio(ctx, tidalID, quality)` which hits `GET /track/` on hifi-api.
2. **BTS manifest** (`application/vnd.tidal.bts`): base64-decode → JSON → direct CDN URL → `newHTTPRangeReader` → `http.ServeContent` → full Range request support (scrubbing, iOS AVPlayer).
3. **DASH manifest** (`application/dash+xml`): parse segment template → stream segments sequentially → `Accept-Ranges: none` — no seeking.

### Virtual library model

Tidal content is stored as normal rows in `media_file`, `album`, and `artist`, with two extra columns added by migration `20260418200000`:

```sql
external_source TEXT  -- "tidal"
external_id     TEXT  -- Tidal's numeric ID as a string
```

Track paths use the scheme `tidal://track/<id>`. A virtual library (`name="Tidal"`, `path="tidal://virtual"`) and a virtual root folder are created on first use so that Subsonic browse queries (which JOIN `media_file → folder`) see Tidal tracks.

The scanner explicitly skips the `tidal://virtual` library path so it never tries to stat virtual URLs.

IDs follow a stable naming scheme:
- Track: `tidal-track-<id>`
- Album: `tidal-album-<id>`
- Artist: `tidal-artist-<id>`

### JIT middleware

`tidalJITMiddleware` wraps every `GET /api/song`, `/api/album`, `/api/artist` request. It extracts the search query from the `_filter` JSON param (react-admin format) or direct `title`/`name` query params, then calls `SyncSearch` synchronously (blocking the request until the sync completes or its own internal timeout elapses) — throttled to once per 10 seconds per query — so the DB is populated before the response is served.

The Subsonic `search3` handler does the same but with a 60 s TTL and popularity-based reranking: local results keep their BM25 order; Tidal results are appended sorted by popularity DESC.


