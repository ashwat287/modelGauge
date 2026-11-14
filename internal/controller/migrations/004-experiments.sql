CREATE TABLE IF NOT EXISTS benchmark_experiments (
 name TEXT PRIMARY KEY,
 spec_sha256 TEXT NOT NULL,
 specification JSONB NOT NULL,
 resolved JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO schema_migrations(version) VALUES ('004-experiments') ON CONFLICT DO NOTHING;
