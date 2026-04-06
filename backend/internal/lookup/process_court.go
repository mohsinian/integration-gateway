package lookup

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/mohsinian/integration-gateway/internal/client"
	"github.com/mohsinian/integration-gateway/internal/model"
	"github.com/mohsinian/integration-gateway/internal/resilience"
	"github.com/mohsinian/integration-gateway/internal/store"
)

func (o *Orchestrator) processCourt(ctx context.Context, c model.Case, src *model.LookupSource) {
	// Check circuit breaker.
	if !o.courtCB.Allow() {
		errMsg := "circuit breaker open"
		o.errorLog.Warn("circuit breaker open — skipping court records",
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

	// Wait for rate limiter token.
	if err := o.courtLimiter.Wait(ctx); err != nil {
		errStr := err.Error()
		o.store.SetSourceResult(ctx, store.SetSourceResultParams{
			SourceID: src.ID,
			Status:   model.SourceStatusFailed,
			Attempts: src.Attempts,
			ErrorMsg: &errStr,
		})
		return
	}

	// Retry the search with exponential backoff.
	var attempts int
	var record *client.CourtRecord
	err := resilience.Do(ctx, resilience.DefaultRetryConfig(), func() error {
		attempts++
		var err error
		record, err = o.courtClient.Search(ctx, *c.CourtCaseNumber)
		return err
	})

	if err != nil {
		o.courtCB.RecordFailure()
		o.errorLog.Error("court records fetch failed",
			slog.String("caseId", c.ID),
			slog.Int("attempts", attempts),
			slog.String("error", err.Error()),
		)
		errStr := err.Error()
		o.store.SetSourceResult(ctx, store.SetSourceResultParams{
			SourceID: src.ID,
			Status:   model.SourceStatusFailed,
			Attempts: src.Attempts + attempts,
			ErrorMsg: &errStr,
		})
		return
	}

	o.courtCB.RecordSuccess()

	// record can be nil (NoFilingFound) — that's a valid success.
	var data json.RawMessage
	if record != nil {
		data, _ = json.Marshal(record)
	}
	o.store.SetSourceResult(ctx, store.SetSourceResultParams{
		SourceID: src.ID,
		Status:   model.SourceStatusSuccess,
		Attempts: src.Attempts + attempts,
		Data:     data,
	})
	o.log.Info("court records fetched",
		slog.String("caseId", c.ID),
		slog.Int("attempts", attempts),
		slog.Bool("noFiling", record == nil),
	)
}
