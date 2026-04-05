package model

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Cases — JSON-facing structs (match cases.json structure)
// ---------------------------------------------------------------------------

// CaseJSON represents a case as it appears in cases.json (nested structure).
type CaseJSON struct {
	ID              string  `json:"id"`
	CaseNumber      string  `json:"caseNumber"`
	Borrower        Borrower `json:"borrower"`
	Property        Property `json:"property"`
	Loan            Loan     `json:"loan"`
	CurrentStage    string   `json:"currentStage"`
	CourtCaseNumber *string  `json:"courtCaseNumber"`
}

type Borrower struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	SSNLast4  string `json:"ssnLast4"`
	DOB       string `json:"dob"`
}

type Property struct {
	Address  string `json:"address"`
	County   string `json:"county"`
	State    string `json:"state"`
	ParcelID string `json:"parcelId"`
}

type Loan struct {
	Number         string  `json:"number"`
	Servicer       string  `json:"servicer"`
	OriginalAmount float64 `json:"originalAmount"`
}

// ---------------------------------------------------------------------------
// Cases — DB-facing struct (matches the flat cases table)
// ---------------------------------------------------------------------------

// Case represents a row in the cases table. Columns are flat — borrower,
// property, and loan fields are stored as individual columns, not nested JSON.
type Case struct {
	ID              string
	CaseNumber      string
	FirstName       string
	LastName        string
	SSNLast4        string
	DOB             string
	Address         string
	County          string
	State           string
	ParcelID        string
	LoanNumber      string
	Servicer        string
	OriginalAmount  float64
	CurrentStage    string
	CourtCaseNumber *string
}

// ---------------------------------------------------------------------------
// Lookup runs (matches 003_lookup_runs.sql)
// ---------------------------------------------------------------------------

type LookupRun struct {
	ID          string     // UUID stored as string
	CaseID      string
	Status      string     // pending, complete, partial, failed
	StartedAt   time.Time
	CompletedAt *time.Time
}

// ---------------------------------------------------------------------------
// Lookup sources (matches 004_lookup_sources.sql)
// ---------------------------------------------------------------------------

type LookupSource struct {
	ID            string           // UUID stored as string
	RunID         string           // UUID stored as string
	Source        string           // property_records, court_records, scra
	Status        string           // pending, success, failed, not_applicable
	Attempts      int
	LastAttemptAt *time.Time
	Data          json.RawMessage  // JSONB from DB
	ErrorMessage  *string
	Reason        *string
	SearchID      *string          // SCRA polling search ID
}

// ---------------------------------------------------------------------------
// Status constants
// ---------------------------------------------------------------------------

const (
	// LookupRun statuses
	StatusPending  = "pending"
	StatusComplete = "complete"
	StatusPartial  = "partial"
	StatusFailed   = "failed"

	// LookupSource statuses
	SourceStatusPending       = "pending"
	SourceStatusSuccess       = "success"
	SourceStatusFailed        = "failed"
	SourceStatusNotApplicable = "not_applicable"

	// Source names
	SourcePropertyRecords = "property_records"
	SourceCourtRecords    = "court_records"
	SourceSCRA            = "scra"
)
