ALTER TABLE model_install_jobs ADD COLUMN IF NOT EXISTS lease_token UUID;
ALTER TABLE model_install_jobs ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;
ALTER TABLE model_install_jobs DROP CONSTRAINT IF EXISTS model_install_jobs_status_check;
ALTER TABLE model_install_jobs ADD CONSTRAINT model_install_jobs_status_check CHECK (status IN ('queued','running','completed','failed','cancelled'));
ALTER TABLE benchmark_runs DROP CONSTRAINT IF EXISTS benchmark_runs_status_check;
ALTER TABLE benchmark_runs ADD CONSTRAINT benchmark_runs_status_check CHECK (status IN ('running','completed','invalid','failed','cancelled'));
INSERT INTO schema_migrations(version) VALUES ('002-job-recovery') ON CONFLICT DO NOTHING;
