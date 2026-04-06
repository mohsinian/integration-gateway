package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mohsinian/integration-gateway/internal/model"
)

// LookupStore handles all database operations for lookup_runs and lookup_sources.
type LookupStore struct {
	pool *pgxpool.Pool
}

// NewLookupStore creates a new LookupStore backed by the given connection pool.
func NewLookupStore(pool *pgxpool.Pool) *LookupStore {
	return &LookupStore{pool: pool}
}

// ---------------------------------------------------------------------------
// Case queries
// ---------------------------------------------------------------------------

// GetCaseByID fetches a case by its ID. Returns nil if not found.
func (s *LookupStore) GetCaseByID(ctx context.Context, id string) (*model.Case, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, case_number, first_name, last_name, ssn_last4, dob,
		       address, county, state, parcel_id, loan_number, servicer,
		       original_amount, current_stage, court_case_number
		FROM cases WHERE id = $1
	`, id)

	var c model.Case
	err := row.Scan(
		&c.ID, &c.CaseNumber, &c.FirstName, &c.LastName, &c.SSNLast4, &c.DOB,
		&c.Address, &c.County, &c.State, &c.ParcelID, &c.LoanNumber, &c.Servicer,
		&c.OriginalAmount, &c.CurrentStage, &c.CourtCaseNumber,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get case %s: %w", id, err)
	}
	return &c, nil
}

// ListCases returns all cases from the database.
func (s *LookupStore) ListCases(ctx context.Context) ([]model.Case, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, case_number, first_name, last_name, ssn_last4, dob,
		       address, county, state, parcel_id, loan_number, servicer,
		       original_amount, current_stage, court_case_number
		FROM cases ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list cases: %w", err)
	}
	defer rows.Close()

	var cases []model.Case
	for rows.Next() {
		var c model.Case
		if err := rows.Scan(
			&c.ID, &c.CaseNumber, &c.FirstName, &c.LastName, &c.SSNLast4, &c.DOB,
			&c.Address, &c.County, &c.State, &c.ParcelID, &c.LoanNumber, &c.Servicer,
			&c.OriginalAmount, &c.CurrentStage, &c.CourtCaseNumber,
		); err != nil {
			return nil, fmt.Errorf("scan case: %w", err)
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// ---------------------------------------------------------------------------
// Lookup run queries
// ---------------------------------------------------------------------------

// GetLookupRunByCaseID returns the lookup run for a case, or nil if none exists.
func (s *LookupStore) GetLookupRunByCaseID(ctx context.Context, caseID string) (*model.LookupRun, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, case_id, status, started_at, completed_at
		FROM lookup_runs WHERE case_id = $1
	`, caseID)

	var r model.LookupRun
	err := row.Scan(&r.ID, &r.CaseID, &r.Status, &r.StartedAt, &r.CompletedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get lookup run for case %s: %w", caseID, err)
	}
	return &r, nil
}

// CreateLookupRun inserts a new lookup run and 3 source rows (property_records,
// court_records, scra) in a single transaction. Returns the created run and sources.
func (s *LookupStore) CreateLookupRun(ctx context.Context, caseID string) (*model.LookupRun, []model.LookupSource, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Insert lookup run.
	var run model.LookupRun
	err = tx.QueryRow(ctx, `
		INSERT INTO lookup_runs (case_id) VALUES ($1)
		RETURNING id, case_id, status, started_at, completed_at
	`, caseID).Scan(&run.ID, &run.CaseID, &run.Status, &run.StartedAt, &run.CompletedAt)
	if err != nil {
		return nil, nil, fmt.Errorf("insert lookup_run: %w", err)
	}

	// Insert 3 source rows.
	sourceNames := []string{model.SourcePropertyRecords, model.SourceCourtRecords, model.SourceSCRA}
	created := make([]model.LookupSource, 0, 3)
	for _, src := range sourceNames {
		var ls model.LookupSource
		err = tx.QueryRow(ctx, `
			INSERT INTO lookup_sources (run_id, source) VALUES ($1, $2)
			RETURNING id, run_id, source, status, attempts, last_attempt_at,
			          data, error_message, reason, search_id
		`, run.ID, src).Scan(
			&ls.ID, &ls.RunID, &ls.Source, &ls.Status, &ls.Attempts,
			&ls.LastAttemptAt, &ls.Data, &ls.ErrorMessage, &ls.Reason, &ls.SearchID,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("insert source %s: %w", src, err)
		}
		created = append(created, ls)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}
	return &run, created, nil
}

// CompleteRun sets the final status and completed_at timestamp.
func (s *LookupStore) CompleteRun(ctx context.Context, runID, status string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE lookup_runs
		SET status = $2, completed_at = NOW()
		WHERE id = $1
	`, runID, status)
	if err != nil {
		return fmt.Errorf("complete run %s: %w", runID, err)
	}
	return nil
}

// ResetRunToPending resets a lookup run's status to pending and clears completed_at.
func (s *LookupStore) ResetRunToPending(ctx context.Context, runID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE lookup_runs
		SET status = 'pending', completed_at = NULL
		WHERE id = $1
	`, runID)
	if err != nil {
		return fmt.Errorf("reset run %s: %w", runID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Lookup source queries
// ---------------------------------------------------------------------------

// GetSourcesByRunID returns all source rows for a lookup run.
func (s *LookupStore) GetSourcesByRunID(ctx context.Context, runID string) ([]model.LookupSource, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, source, status, attempts, last_attempt_at,
		       data, error_message, reason, search_id
		FROM lookup_sources WHERE run_id = $1
		ORDER BY source
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("get sources for run %s: %w", runID, err)
	}
	defer rows.Close()

	var sources []model.LookupSource
	for rows.Next() {
		var ls model.LookupSource
		if err := rows.Scan(
			&ls.ID, &ls.RunID, &ls.Source, &ls.Status, &ls.Attempts,
			&ls.LastAttemptAt, &ls.Data, &ls.ErrorMessage, &ls.Reason, &ls.SearchID,
		); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, ls)
	}
	return sources, nil
}

// SetSourceResultParams contains all fields for updating a source row after a
// lookup attempt (success, failure, or not-applicable).
type SetSourceResultParams struct {
	SourceID string
	Status   string
	Attempts int
	Data     json.RawMessage
	ErrorMsg *string
	Reason   *string
	SearchID *string
}

// SetSourceResult updates a source row with the outcome of a lookup attempt.
// It sets last_attempt_at to the current time.
func (s *LookupStore) SetSourceResult(ctx context.Context, p SetSourceResultParams) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE lookup_sources
		SET status = $2, attempts = $3, last_attempt_at = $4,
		    data = $5, error_message = $6, reason = $7, search_id = $8
		WHERE id = $1
	`,
		p.SourceID,
		p.Status,
		p.Attempts,
		time.Now(),
		p.Data,
		p.ErrorMsg,
		p.Reason,
		p.SearchID,
	)
	if err != nil {
		return fmt.Errorf("update source %s: %w", p.SourceID, err)
	}
	return nil
}

// ResetSourcesToPending sets the status of the given source IDs back to "pending".
// Used when re-triggering failed sources.
func (s *LookupStore) ResetSourcesToPending(ctx context.Context, sourceIDs []string) error {
	for _, id := range sourceIDs {
		_, err := s.pool.Exec(ctx, `
			UPDATE lookup_sources
			SET status = 'pending', error_message = NULL, data = NULL, reason = NULL
			WHERE id = $1
		`, id)
		if err != nil {
			return fmt.Errorf("reset source %s: %w", id, err)
		}
	}
	return nil
}

// GetRetryableSources returns sources that are in "pending" or "failed" status
// for the given run — i.e., sources that should be (re-)attempted.
func (s *LookupStore) GetRetryableSources(ctx context.Context, runID string) ([]model.LookupSource, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, source, status, attempts, last_attempt_at,
		       data, error_message, reason, search_id
		FROM lookup_sources
		WHERE run_id = $1 AND status IN ('pending', 'failed')
		ORDER BY source
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("get retryable sources for run %s: %w", runID, err)
	}
	defer rows.Close()

	var sources []model.LookupSource
	for rows.Next() {
		var ls model.LookupSource
		if err := rows.Scan(
			&ls.ID, &ls.RunID, &ls.Source, &ls.Status, &ls.Attempts,
			&ls.LastAttemptAt, &ls.Data, &ls.ErrorMessage, &ls.Reason, &ls.SearchID,
		); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, ls)
	}
	return sources, nil
}
