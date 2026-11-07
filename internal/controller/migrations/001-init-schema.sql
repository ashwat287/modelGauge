CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nodes (
  node_id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  node_kind TEXT NOT NULL CHECK (node_kind IN ('physical', 'virtual')),
  execution_mode TEXT NOT NULL CHECK (execution_mode IN ('native-isolated', 'lima-linux')),
  hardware JSONB NOT NULL DEFAULT '{}'::jsonb,
  agent_version TEXT,
  status TEXT NOT NULL DEFAULT 'unknown'
    CHECK (status IN ('unknown', 'online', 'busy', 'offline')),
  last_seen_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS benchmark_plans (
  plan_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  node_id TEXT NOT NULL REFERENCES nodes(node_id),
  status TEXT NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'leased', 'running', 'completed', 'failed', 'cancelled')),
  specification JSONB NOT NULL,
  lease_token UUID,
  lease_expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS node_models (
  node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
  model_id TEXT NOT NULL,
  model_path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes BIGINT NOT NULL,
  discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (node_id, model_id)
);

CREATE TABLE IF NOT EXISTS model_install_jobs (
  job_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  node_id TEXT NOT NULL REFERENCES nodes(node_id),
  model_id TEXT NOT NULL,
  source_url TEXT NOT NULL,
  expected_sha256 TEXT,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','completed','failed')),
  error_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS benchmark_runs (
  run_id TEXT PRIMARY KEY,
  plan_id UUID REFERENCES benchmark_plans(plan_id),
  node_id TEXT NOT NULL REFERENCES nodes(node_id),
  execution_mode TEXT NOT NULL,
  status TEXT NOT NULL
    CHECK (status IN ('running', 'completed', 'invalid', 'failed')),
  runtime_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  invalid_reason TEXT
);

CREATE TABLE IF NOT EXISTS benchmark_cases (
  run_id TEXT NOT NULL REFERENCES benchmark_runs(run_id) ON DELETE CASCADE,
  case_id TEXT NOT NULL,
  threads INTEGER NOT NULL CHECK (threads > 0),
  prompt_tokens INTEGER NOT NULL CHECK (prompt_tokens >= 0),
  generation_tokens INTEGER NOT NULL CHECK (generation_tokens >= 0),
  repetitions INTEGER NOT NULL CHECK (repetitions > 0),
  average_tokens_per_second DOUBLE PRECISION,
  stddev_tokens_per_second DOUBLE PRECISION,
  raw_result JSONB NOT NULL,
  PRIMARY KEY (run_id, case_id)
);

CREATE TABLE IF NOT EXISTS memory_measurements (
  run_id TEXT PRIMARY KEY REFERENCES benchmark_runs(run_id) ON DELETE CASCADE,
  case_id TEXT NOT NULL,
  peak_rss_bytes BIGINT NOT NULL CHECK (peak_rss_bytes > 0),
  safe_reservation_bytes BIGINT NOT NULL CHECK (safe_reservation_bytes > 0),
  raw_time_output TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS host_snapshots (
  snapshot_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id TEXT REFERENCES benchmark_runs(run_id) ON DELETE CASCADE,
  snapshot_phase TEXT NOT NULL
    CHECK (snapshot_phase IN ('preflight', 'postflight', 'periodic')),
  captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  metrics JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS artifacts (
  artifact_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id TEXT NOT NULL REFERENCES benchmark_runs(run_id) ON DELETE CASCADE,
  artifact_name TEXT NOT NULL,
  content_type TEXT NOT NULL,
  storage_uri TEXT NOT NULL,
  sha256 TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_id, artifact_name)
);

CREATE TABLE IF NOT EXISTS recommendations (
  recommendation_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  node_id TEXT NOT NULL REFERENCES nodes(node_id),
  model_id TEXT NOT NULL,
  execution_mode TEXT NOT NULL,
  context_tokens INTEGER NOT NULL CHECK (context_tokens > 0),
  prompt_threads INTEGER,
  generation_threads INTEGER,
  prompt_tokens_per_second DOUBLE PRECISION,
  generation_tokens_per_second DOUBLE PRECISION,
  required_memory_bytes BIGINT,
  fit_status TEXT NOT NULL
    CHECK (fit_status IN ('fits', 'conditional', 'does-not-fit')),
  source_run_ids TEXT[] NOT NULL,
  algorithm_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS benchmark_plans_queue_idx
  ON benchmark_plans (status, created_at);

CREATE INDEX IF NOT EXISTS benchmark_runs_node_idx
  ON benchmark_runs (node_id, completed_at DESC);

CREATE INDEX IF NOT EXISTS recommendations_node_model_idx
  ON recommendations (node_id, model_id, context_tokens, created_at DESC);

INSERT INTO schema_migrations (version)
VALUES ('001-init-schema')
ON CONFLICT (version) DO NOTHING;
