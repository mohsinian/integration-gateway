package lookup

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/mohsinian/integration-gateway/internal/client"
	"github.com/mohsinian/integration-gateway/internal/model"
	"github.com/mohsinian/integration-gateway/internal/resilience"
	"github.com/mohsinian/integration-gateway/internal/store"
)

func (o *Orchestrator) processSCRA(ctx context.Context, c model.Case, src *model.LookupSource) {
	// Check circuit breaker.
	if !o.scraCB.Allow() {
		errMsg := "circuit breaker open"
		o.errorLog.Warn("circuit breaker open — skipping SCRA",
			slog.String("caseId", c.ID),
		)
		o.store.SetSourceResult(ctx, store.SetSourceResultParams{
			SourceID: src.ID,
			Status:   model.SourceStatusFailed,
			Attempts: src.Attempts,
			ErrorMsg: &errMsg,
		})
		return
	}

	// --- Phase 1: Submit search with retry (up to 3 attempts) ---
	submitCfg := resilience.RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Second,
		MaxDelay:     10 * time.Second,
		Multiplier:   2.0,
	}

	var submitAttempts int
	var searchID string
	err := resilience.Do(ctx, submitCfg, func() error {
		submitAttempts++
		var err error
		searchID, err = o.scraClient.SubmitSearch(ctx, client.SCRASearchRequest{
			LastName:  c.LastName,
			FirstName: c.FirstName,
			SSNLast4:  c.SSNLast4,
			DOB:       c.DOB,
		})
		return err
	})

	if err != nil {
		o.scraCB.RecordFailure()
		o.errorLog.Error("SCRA submit failed",
			slog.String("caseId", c.ID),
			slog.Int("attempts", submitAttempts),
			slog.String("error", err.Error()),
		)
		errStr := err.Error()
		o.store.SetSourceResult(ctx, store.SetSourceResultParams{
			SourceID: src.ID,
			Status:   model.SourceStatusFailed,
			Attempts: src.Attempts + submitAttempts,
			ErrorMsg: &errStr,
		})
		return
	}

	totalAttempts := src.Attempts + submitAttempts

	// --- Phase 2: Poll for results — every 1s, up to 30s total ---
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			o.errorLog.Error("SCRA poll timeout",
				slog.String("caseId", c.ID),
				slog.String("searchId", searchID),
			)
			errStr := "poll timeout after 30 seconds"
			o.store.SetSourceResult(ctx, store.SetSourceResultParams{
				SourceID: src.ID,
				Status:   model.SourceStatusFailed,
				Attempts: totalAttempts,
				ErrorMsg: &errStr,
				SearchID: &searchID,
			})
			return

		case <-ticker.C:
			totalAttempts++
			result, status, pollErr := o.scraClient.PollResult(pollCtx, searchID)

			// Search permanently failed — stop polling.
			if status == "error" {
				o.scraCB.RecordFailure()
				errStr := pollErr.Error()
				o.store.SetSourceResult(ctx, store.SetSourceResultParams{
					SourceID: src.ID,
					Status:   model.SourceStatusFailed,
					Attempts: totalAttempts,
					ErrorMsg: &errStr,
					SearchID: &searchID,
				})
				o.errorLog.Error("SCRA search error",
					slog.String("caseId", c.ID),
					slog.String("searchId", searchID),
					slog.String("error", errStr),
				)
				return
			}

			// Search complete — store result.
			if status == "complete" {
				o.scraCB.RecordSuccess()
				data, _ := json.Marshal(result)
				o.store.SetSourceResult(ctx, store.SetSourceResultParams{
					SourceID: src.ID,
					Status:   model.SourceStatusSuccess,
					Attempts: totalAttempts,
					Data:     data,
					SearchID: &searchID,
				})
				o.log.Info("SCRA check complete",
					slog.String("caseId", c.ID),
					slog.String("searchId", searchID),
					slog.Bool("activeDuty", result.ActiveDuty),
				)
				return
			}

			// Transport/protocol error — check if permanent.
			if pollErr != nil && status == "" {
				if resilience.IsPermanentError(pollErr) {
					o.scraCB.RecordFailure()
					errStr := pollErr.Error()
					o.store.SetSourceResult(ctx, store.SetSourceResultParams{
						SourceID: src.ID,
						Status:   model.SourceStatusFailed,
						Attempts: totalAttempts,
						ErrorMsg: &errStr,
						SearchID: &searchID,
					})
					return
				}
				// Transient — continue polling.
				o.log.Warn("SCRA poll transient error, retrying",
					slog.String("caseId", c.ID),
					slog.String("searchId", searchID),
					slog.String("error", pollErr.Error()),
				)
				continue
			}

			// status == "pending" — continue polling.
		}
	}
}
