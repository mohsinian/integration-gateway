CREATE TABLE lookup_sources (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          UUID NOT NULL REFERENCES lookup_runs(id) ON DELETE CASCADE,
    source          TEXT NOT NULL,               -- "property_records", "court_records", "scra"
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending, success, failed, not_applicable
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    data            JSONB,                       -- The actual fetched data
    error_message   TEXT,                        -- Last error if failed
    reason          TEXT,                        -- For not_applicable: why it was skipped
    search_id       TEXT,                        -- For SCRA: the polling search ID
    UNIQUE(run_id, source)
);

CREATE INDEX idx_lookup_sources_run ON lookup_sources(run_id);
CREATE INDEX idx_lookup_runs_case ON lookup_runs(case_id);
