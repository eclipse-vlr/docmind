CREATE TABLE documents (
    id uuid PRIMARY KEY,
    original_name text NOT NULL CHECK (octet_length(original_name) BETWEEN 1 AND 255),
    mime_type text NOT NULL CHECK (mime_type IN ('text/plain', 'text/markdown', 'application/pdf')),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 10485760),
    storage_key text NOT NULL UNIQUE CHECK (storage_key = id::text),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'processing', 'ready', 'failed')),
    error_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX documents_created_idx ON documents (created_at DESC, id DESC);

CREATE TABLE ingestion_jobs (
    id uuid PRIMARY KEY,
    document_id uuid NOT NULL UNIQUE REFERENCES documents(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'processing', 'completed', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_run_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    error_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ingestion_jobs_pending_idx ON ingestion_jobs (next_run_at, id) WHERE status = 'queued';
CREATE INDEX ingestion_jobs_lease_idx ON ingestion_jobs (lease_until) WHERE status = 'processing';

-- No FK: the cleanup intent must survive deletion of the document.
CREATE TABLE file_deletions (
    storage_key text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);
