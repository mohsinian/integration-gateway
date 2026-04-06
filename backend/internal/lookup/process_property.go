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

func (o *Orchestrator) processProperty(ctx context.Context, c model.Case, src *model.LookupSource) {
	// Check circuit breaker.
	if !o.propertyCB.Allow() {
		errMsg := "circuit breaker open"
		o.errorLog.Warn("circuit breaker open — skipping property records",
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

	// Retry the fetch with exponential backoff.
	var attempts int
	var record *client.PropertyRecord
	err := resilience.Do(ctx, resilience.DefaultRetryConfig(), func() error {
		attempts++
		var err error
		record, err = o.propertyClient.Fetch(ctx, c.State, c.County, c.ParcelID)
		return err
	})

	if err != nil {
		o.propertyCB.RecordFailure()
		o.errorLog.Error("property records fetch failed",
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

	o.propertyCB.RecordSuccess()
	data, _ := json.Marshal(record)
	o.store.SetSourceResult(ctx, store.SetSourceResultParams{
		SourceID: src.ID,
		Status:   model.SourceStatusSuccess,
		Attempts: src.Attempts + attempts,
		Data:     data,
	})
	o.log.Info("property records fetched",
		slog.String("caseId", c.ID),
		slog.Int("attempts", attempts),
	)
}
