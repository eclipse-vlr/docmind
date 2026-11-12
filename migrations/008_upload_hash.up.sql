-- Existing files are not read or reindexed during migration.
ALTER TABLE documents ADD COLUMN content_hash text;
CREATE UNIQUE INDEX documents_content_hash ON documents(content_hash) WHERE content_hash IS NOT NULL;
