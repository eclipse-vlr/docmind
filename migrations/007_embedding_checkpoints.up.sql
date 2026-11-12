ALTER TABLE ingestion_jobs ADD COLUMN index_generation bigint NOT NULL DEFAULT 1;
CREATE TABLE embedding_checkpoints (
    job_id uuid NOT NULL REFERENCES ingestion_jobs(id) ON DELETE CASCADE,
    index_generation bigint NOT NULL,
    model text NOT NULL CHECK (length(model) BETWEEN 1 AND 128),
    text_hash text NOT NULL CHECK (length(text_hash) = 64),
    embedding jsonb NOT NULL,
    PRIMARY KEY (job_id, index_generation, model, text_hash)
);
