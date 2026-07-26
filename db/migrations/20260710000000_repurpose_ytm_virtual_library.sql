-- +goose Up
-- Bringing the Monochroma (YouTube Music) database into the Tidal build.
--
-- That database was built while the audio provider was YouTube Music, which
-- registered a virtual library at path 'ytm://virtual'. The Tidal build only
-- registers the 'tidal://' storage scheme, so that row is unresolvable and
-- Navidrome logs "schema 'ytm' not registered" on every scan/watch.
--
-- Repurpose the existing YTM virtual library as the Tidal virtual library
-- instead of dropping it: every media_file/album/artist already points at this
-- library_id, so renaming the row (rather than deleting it) preserves all of
-- them. core/tidal reuses any library whose path is 'tidal://virtual', so it
-- adopts this same row on the next search. Guarded so it is a no-op when a
-- Tidal library already exists (fresh installs) or no YTM library is present.
UPDATE library
SET name = 'Tidal', path = 'tidal://virtual'
WHERE path = 'ytm://virtual'
  AND NOT EXISTS (SELECT 1 FROM library WHERE path = 'tidal://virtual');

-- +goose Down
UPDATE library
SET name = 'YouTube Music', path = 'ytm://virtual'
WHERE path = 'tidal://virtual';
