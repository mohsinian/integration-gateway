CREATE TABLE IF NOT EXISTS schema_version (
    id            SERIAL PRIMARY KEY,
    filename      TEXT NOT NULL UNIQUE,
    status        TEXT NOT NULL,       -- "success" or "failed"
    error_message TEXT,                -- NULL if success, error details if failed
    applied_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Always insert this file itself as the first successful migration
INSERT INTO schema_version (filename, status)
VALUES ('001_schema_version.sql', 'success')
ON CONFLICT (filename) DO NOTHING;
