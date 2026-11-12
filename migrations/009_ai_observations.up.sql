ALTER TABLE ai_usage_calls ADD COLUMN model text NOT NULL DEFAULT '';
ALTER TABLE ai_usage_calls ADD COLUMN prompt_version text NOT NULL DEFAULT '';
ALTER TABLE ai_usage_calls ADD COLUMN request_id text NOT NULL DEFAULT '';
ALTER TABLE ai_usage_calls ADD COLUMN duration_ms bigint NOT NULL DEFAULT 0;
ALTER TABLE ai_usage_calls ADD COLUMN http_status integer NOT NULL DEFAULT 0;
