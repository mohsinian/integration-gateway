CREATE TABLE cases (
    id                TEXT PRIMARY KEY,           -- "case-001"
    case_number       TEXT NOT NULL,
    first_name        TEXT NOT NULL,
    last_name         TEXT NOT NULL,
    ssn_last4         TEXT NOT NULL,
    dob               TEXT NOT NULL,
    address           TEXT NOT NULL,
    county            TEXT NOT NULL,
    state             TEXT NOT NULL,
    parcel_id         TEXT NOT NULL,
    loan_number       TEXT NOT NULL,
    servicer          TEXT NOT NULL,
    original_amount   DOUBLE PRECISION NOT NULL,
    current_stage     TEXT NOT NULL,
    court_case_number TEXT                        -- NULL for pre-filing cases
);
