-- +goose Up

-- Add external source tracking columns to media_file
ALTER TABLE media_file ADD COLUMN external_source TEXT NOT NULL DEFAULT '';
ALTER TABLE media_file ADD COLUMN external_id TEXT NOT NULL DEFAULT '';

-- Add external source tracking columns to album
ALTER TABLE album ADD COLUMN external_source TEXT NOT NULL DEFAULT '';
ALTER TABLE album ADD COLUMN external_id TEXT NOT NULL DEFAULT '';

-- Add external source tracking columns to artist
ALTER TABLE artist ADD COLUMN external_source TEXT NOT NULL DEFAULT '';
ALTER TABLE artist ADD COLUMN external_id TEXT NOT NULL DEFAULT '';

-- Create indexes for efficient lookups by external source
CREATE INDEX IF NOT EXISTS idx_media_file_external ON media_file(external_source, external_id);
CREATE INDEX IF NOT EXISTS idx_album_external ON album(external_source, external_id);
CREATE INDEX IF NOT EXISTS idx_artist_external ON artist(external_source, external_id);

-- +goose Down

-- Note: SQLite does not support DROP COLUMN, so these would need to be handled via table recreation
