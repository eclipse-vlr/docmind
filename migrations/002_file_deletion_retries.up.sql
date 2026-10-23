ALTER TABLE file_deletions
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX file_deletions_due_idx ON file_deletions (next_attempt_at, storage_key);
