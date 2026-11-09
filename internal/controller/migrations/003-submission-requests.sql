CREATE TABLE IF NOT EXISTS submission_requests (
 request_id TEXT PRIMARY KEY,
 request_sha256 TEXT NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('benchmark', 'install')),
 resource_id UUID NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO schema_migrations(version) VALUES ('003-submission-requests') ON CONFLICT DO NOTHING;
