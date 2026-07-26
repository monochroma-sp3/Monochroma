-- +goose Up

-- Populate order_artist_name for Tidal tracks where it is empty.
-- Mirrors the logic of SanitizeFieldForSortingNoArticle: strip leading articles, then lowercase.
-- Articles: "The El La Los Las Le Les Os As O A" (default IgnoredArticles)
UPDATE media_file
SET order_artist_name =
    CASE
        WHEN lower(artist) LIKE 'the %'  THEN lower(trim(substr(artist, 5)))
        WHEN lower(artist) LIKE 'los %'  THEN lower(trim(substr(artist, 5)))
        WHEN lower(artist) LIKE 'las %'  THEN lower(trim(substr(artist, 5)))
        WHEN lower(artist) LIKE 'les %'  THEN lower(trim(substr(artist, 5)))
        WHEN lower(artist) LIKE 'el %'   THEN lower(trim(substr(artist, 4)))
        WHEN lower(artist) LIKE 'la %'   THEN lower(trim(substr(artist, 4)))
        WHEN lower(artist) LIKE 'le %'   THEN lower(trim(substr(artist, 4)))
        WHEN lower(artist) LIKE 'os %'   THEN lower(trim(substr(artist, 4)))
        WHEN lower(artist) LIKE 'as %'   THEN lower(trim(substr(artist, 4)))
        WHEN lower(artist) LIKE 'o %'    THEN lower(trim(substr(artist, 3)))
        WHEN lower(artist) LIKE 'a %'    THEN lower(trim(substr(artist, 3)))
        ELSE lower(trim(artist))
    END
WHERE id LIKE 'tidal-%'
  AND (order_artist_name IS NULL OR order_artist_name = '');

-- +goose Down
UPDATE media_file
SET order_artist_name = ''
WHERE id LIKE 'tidal-%';
