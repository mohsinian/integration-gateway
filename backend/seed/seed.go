package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mohsinian/integration-gateway/internal/model"
)

// Cases reads cases.json and inserts all records into the cases table.
// It is idempotent: if the table already contains rows it returns immediately.
func Cases(ctx context.Context, pool *pgxpool.Pool, filePath string, log *slog.Logger) error {
	// Skip if already seeded.
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM cases").Scan(&count); err != nil {
		return fmt.Errorf("check cases count: %w", err)
	}
	if count > 0 {
		log.Info("cases table already seeded, skipping", slog.Int("rows", count))
		return nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read cases file %s: %w", filePath, err)
	}

	var jsonCases []model.CaseJSON
	if err := json.Unmarshal(data, &jsonCases); err != nil {
		return fmt.Errorf("parse cases json: %w", err)
	}

	const insertSQL = `
		INSERT INTO cases (id, case_number, first_name, last_name, ssn_last4, dob,
		                   address, county, state, parcel_id, loan_number, servicer,
		                   original_amount, current_stage, court_case_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`

	batch := &pgx.Batch{}
	for _, c := range jsonCases {
		batch.Queue(insertSQL,
			c.ID,
			c.CaseNumber,
			c.Borrower.FirstName,
			c.Borrower.LastName,
			c.Borrower.SSNLast4,
			c.Borrower.DOB,
			c.Property.Address,
			c.Property.County,
			c.Property.State,
			c.Property.ParcelID,
			c.Loan.Number,
			c.Loan.Servicer,
			c.Loan.OriginalAmount,
			c.CurrentStage,
			c.CourtCaseNumber,
		)
	}

	results := pool.SendBatch(ctx, batch)
	defer results.Close()

	for i := range jsonCases {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("insert case %s: %w", jsonCases[i].ID, err)
		}
	}

	log.Info("seeded cases table", slog.Int("count", len(jsonCases)))
	return nil
}
