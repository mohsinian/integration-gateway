CREATE TABLE lookup_runs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id       TEXT NOT NULL REFERENCES cases(id),
    status        TEXT NOT NULL DEFAULT 'pending',  -- pending, complete, partial, failed
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at  TIMESTAMPTZ,
    UNIQUE(case_id)  -- one lookup run per case (idempotency guarantee)
);
