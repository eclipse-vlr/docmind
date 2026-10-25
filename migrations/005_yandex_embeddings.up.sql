-- Embeddings are derived data. Keep source files and chunk text, then rebuild
-- the incompatible 1536-dimensional index in the new 256-dimensional space.
ALTER TABLE document_chunks ALTER COLUMN embedding TYPE public.vector(256)
    USING NULL::public.vector(256);

-- Advance the fence before attempt numbers are reused by the new workers.
UPDATE ingestion_jobs SET status='queued', attempts=0, lease_until=NULL,
    generation=generation+1, next_run_at=now(), error_message='', updated_at=now()
WHERE document_id IN (SELECT id FROM documents WHERE status <> 'failed');
UPDATE documents SET status='queued', embedding_model='', error_message='', updated_at=now()
WHERE status <> 'failed';
