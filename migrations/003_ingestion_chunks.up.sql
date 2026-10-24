CREATE TABLE document_chunks (
    id uuid PRIMARY KEY,
    document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    page_number integer CHECK (page_number > 0),
    content text NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 4000),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (document_id, ordinal)
);

ALTER TABLE ingestion_jobs ADD CONSTRAINT ingestion_jobs_lease_state
    CHECK ((status = 'processing') = (lease_until IS NOT NULL));
