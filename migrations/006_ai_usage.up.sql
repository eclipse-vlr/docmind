CREATE TABLE ai_usage_days (
 day date PRIMARY KEY,
 limit_nanorub bigint NOT NULL CHECK (limit_nanorub > 0),
 estimated_nanorub bigint NOT NULL DEFAULT 0 CHECK (estimated_nanorub >= 0),
 reserved_nanorub bigint NOT NULL DEFAULT 0 CHECK (reserved_nanorub >= 0)
);

-- A durable pending/unknown request keeps its full reservation. No expiry can
-- prove that the upstream did not bill a timed out or interrupted request.
CREATE TABLE ai_usage_calls (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 day date NOT NULL REFERENCES ai_usage_days(day),
 kind text NOT NULL CHECK (kind IN ('chat','embedding_doc','embedding_query')),
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','recorded','unknown')),
 reserved_nanorub bigint NOT NULL CHECK (reserved_nanorub > 0),
 input_rate bigint NOT NULL CHECK (input_rate > 0),
 output_rate bigint NOT NULL CHECK (output_rate >= 0),
 input_tokens bigint,
 output_tokens bigint,
 estimated_nanorub bigint,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 CHECK ((status='recorded' AND input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND estimated_nanorub IS NOT NULL AND input_tokens>=0 AND output_tokens>=0 AND estimated_nanorub>=0)
     OR (status IN ('pending','unknown') AND input_tokens IS NULL AND output_tokens IS NULL AND estimated_nanorub IS NULL))
);
CREATE INDEX ai_usage_calls_day ON ai_usage_calls(day);
