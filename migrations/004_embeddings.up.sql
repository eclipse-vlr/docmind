ALTER TABLE document_chunks ADD COLUMN embedding public.vector(1536);
ALTER TABLE document_chunks ADD CONSTRAINT document_chunks_embedding_nonzero
    CHECK (embedding IS NULL OR public.vector_norm(embedding) > 0);

ALTER TABLE documents ADD COLUMN embedding_model text NOT NULL DEFAULT '';
ALTER TABLE ingestion_jobs ADD COLUMN generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0);

-- Stage 3 stored text only: keep sources and fragments while scheduling indexing.
UPDATE ingestion_jobs SET status='queued', attempts=0, lease_until=NULL,
    generation=generation+1, next_run_at=now(), error_message='', updated_at=now()
WHERE document_id IN (SELECT id FROM documents WHERE status IN ('ready','processing'));
UPDATE documents SET status='queued', error_message='', updated_at=now()
WHERE status IN ('ready','processing');

-- Older workers cannot publish text-only results after this migration.
ALTER TABLE documents ADD CONSTRAINT documents_ready_model
    CHECK (status <> 'ready' OR embedding_model <> '');
