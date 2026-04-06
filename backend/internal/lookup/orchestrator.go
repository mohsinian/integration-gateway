package lookup

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/mohsinian/integration-gateway/internal/client"
	"github.com/mohsinian/integration-gateway/internal/model"
	"github.com/mohsinian/integration-gateway/internal/resilience"
	"github.com/mohsinian/integration-gateway/internal/store"
)

// LookupResult packages the data that the HTTP handler needs to build a response.
type LookupResult struct {
	Case    *model.Case
	Run     *model.LookupRun
	Sources []model.LookupSource
}

// Orchestrator coordinates fetching from all external sources for a given case.
// One instance is shared across all HTTP requests.
type Orchestrator struct {
	store          *store.LookupStore
	propertyClient *client.PropertyClient
	courtClient    *client.CourtClient
	scraClient     *client.SCRAClient
	propertyCB     *resilience.CircuitBreaker
	courtCB        *resilience.CircuitBreaker
	scraCB         *resilience.CircuitBreaker
	courtLimiter   *resilience.RateLimiter
	log            *slog.Logger
	errorLog       *slog.Logger
}

// NewOrchestrator wires up the orchestrator with all its dependencies.
func NewOrchestrator(
	s *store.LookupStore,
	propertyClient *client.PropertyClient,
	courtClient *client.CourtClient,
	scraClient *client.SCRAClient,
	propertyCB *resilience.CircuitBreaker,
	courtCB *resilience.CircuitBreaker,
	scraCB *resilience.CircuitBreaker,
	courtLimiter *resilience.RateLimiter,
	log *slog.Logger,
	errorLog *slog.Logger,
) *Orchestrator {
	return &Orchestrator{
		store:          s,
		propertyClient: propertyClient,
		courtClient:    courtClient,
		scraClient:     scraClient,
		propertyCB:     propertyCB,
		courtCB:        courtCB,
		scraCB:         scraCB,
		courtLimiter:   courtLimiter,
		log:            log,
		errorLog:       errorLog,
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// TriggerLookup initiates a lookup for the given case.
//
// Idempotency rules:
//   - No existing run → create one and start async lookup → (result, 202, nil)
//   - Complete run → return existing result → (result, 200, nil)
//   - Pending run  → return current state (already in progress) → (result, 200, nil)
//   - Partial/Failed run → re-trigger only failed sources → (result, 202, nil)
func (o *Orchestrator) TriggerLookup(caseID string) (*LookupResult, int, error) {
	ctx := context.Background()

	// 1. Get the case.
	c, err := o.store.GetCaseByID(ctx, caseID)
	if err != nil {
		return nil, 0, fmt.Errorf("get case: %w", err)
	}
	if c == nil {
		return nil, 0, fmt.Errorf("case %s not found", caseID)
	}

	// 2. Check for existing lookup run.
	run, err := o.store.GetLookupRunByCaseID(ctx, caseID)
	if err != nil {
		return nil, 0, fmt.Errorf("get lookup run: %w", err)
	}

	// 3. Handle based on existing run status.
	if run != nil {
		sources, err := o.store.GetSourcesByRunID(ctx, run.ID)
		if err != nil {
			return nil, 0, fmt.Errorf("get sources: %w", err)
		}

		switch run.Status {
		case model.StatusComplete:
			// Already done — return the cached result.
			return &LookupResult{Case: c, Run: run, Sources: sources}, 200, nil

		case model.StatusPending:
			// Already in progress — return current state.
			return &LookupResult{Case: c, Run: run, Sources: sources}, 200, nil

		case model.StatusPartial, model.StatusFailed:
			// Re-trigger only failed/pending sources.
			retryable, err := o.store.GetRetryableSources(ctx, run.ID)
			if err != nil {
				return nil, 0, fmt.Errorf("get retryable sources: %w", err)
			}
			if len(retryable) == 0 {
				// Everything succeeded somehow — mark complete.
				o.store.CompleteRun(ctx, run.ID, model.StatusComplete)
				run.Status = model.StatusComplete
				return &LookupResult{Case: c, Run: run, Sources: sources}, 200, nil
			}

			// Reset failed sources to pending and re-trigger.
			var ids []string
			for _, s := range retryable {
				ids = append(ids, s.ID)
			}
			if err := o.store.ResetSourcesToPending(ctx, ids); err != nil {
				return nil, 0, fmt.Errorf("reset sources: %w", err)
			}
			if err := o.store.ResetRunToPending(ctx, run.ID); err != nil {
				return nil, 0, fmt.Errorf("reset run: %w", err)
			}
			run.Status = model.StatusPending

			// Reload sources after reset for accurate response.
			sources, _ = o.store.GetSourcesByRunID(ctx, run.ID)

			o.log.Info("re-triggering failed sources",
				slog.String("caseId", caseID),
				slog.Int("sources", len(retryable)),
			)
			go o.runLookup(*c, *run)
			return &LookupResult{Case: c, Run: run, Sources: sources}, 202, nil
		}
	}

	// 4. No existing run — create one.
	run, sources, err := o.store.CreateLookupRun(ctx, caseID)
	if err != nil {
		return nil, 0, fmt.Errorf("create lookup run: %w", err)
	}

	o.log.Info("starting lookup",
		slog.String("caseId", caseID),
		slog.String("runId", run.ID),
	)

	// 5. Launch async lookup.
	go o.runLookup(*c, *run)

	return &LookupResult{Case: c, Run: run, Sources: sources}, 202, nil
}

// GetStatus returns the current lookup status for a case.
// Returns a result with a nil Run if no lookup has been triggered yet.
func (o *Orchestrator) GetStatus(caseID string) (*LookupResult, error) {
	ctx := context.Background()

	c, err := o.store.GetCaseByID(ctx, caseID)
	if err != nil {
		return nil, fmt.Errorf("get case: %w", err)
	}
	if c == nil {
		return nil, fmt.Errorf("case %s not found", caseID)
	}

	run, err := o.store.GetLookupRunByCaseID(ctx, caseID)
	if err != nil {
		return nil, fmt.Errorf("get lookup run: %w", err)
	}
	if run == nil {
		return &LookupResult{Case: c}, nil
	}

	sources, err := o.store.GetSourcesByRunID(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("get sources: %w", err)
	}

	return &LookupResult{Case: c, Run: run, Sources: sources}, nil
}

// ---------------------------------------------------------------------------
// Async lookup goroutine
// ---------------------------------------------------------------------------

// runLookup is the async goroutine that does the actual work.
// It processes all applicable sources concurrently using errgroup.
func (o *Orchestrator) runLookup(c model.Case, run model.LookupRun) {
	ctx := context.Background()

	// Load source rows.
	sources, err := o.store.GetSourcesByRunID(ctx, run.ID)
	if err != nil {
		o.errorLog.Error("failed to load sources",
			slog.String("runId", run.ID),
			slog.String("error", err.Error()),
		)
		return
	}

	// Launch all applicable sources concurrently.
	g, gctx := errgroup.WithContext(ctx)

	for i := range sources {
		src := &sources[i]
		// Skip sources that are already resolved.
		if src.Status == model.SourceStatusNotApplicable || src.Status == model.SourceStatusSuccess {
			continue
		}
		s := *src // capture by value to avoid race
		g.Go(func() error {
			o.processSource(gctx, c, &s)
			return nil // each source manages its own DB state; no error propagation needed
		})
	}

	g.Wait()

	// Compute and persist overall status from DB (authoritative).
	sources, _ = o.store.GetSourcesByRunID(ctx, run.ID)
	overall := computeOverallStatus(sources)
	o.store.CompleteRun(ctx, run.ID, overall)

	o.log.Info("lookup complete",
		slog.String("caseId", c.ID),
		slog.String("runId", run.ID),
		slog.String("status", overall),
	)
}

// ---------------------------------------------------------------------------
// Per-source processing
// ---------------------------------------------------------------------------

// processSource dispatches to the correct handler based on source type.
// For court records, it checks applicability first.
func (o *Orchestrator) processSource(ctx context.Context, c model.Case, src *model.LookupSource) {
	switch src.Source {
	case model.SourcePropertyRecords:
		o.processProperty(ctx, c, src)
	case model.SourceCourtRecords:
		if c.CourtCaseNumber == nil {
			reason := "No court case number (pre-filing stage)"
			o.store.SetSourceResult(ctx, store.SetSourceResultParams{
				SourceID: src.ID,
				Status:   model.SourceStatusNotApplicable,
				Attempts: src.Attempts,
				Reason:   &reason,
			})
			return
		}
		o.processCourt(ctx, c, src)
	case model.SourceSCRA:
		o.processSCRA(ctx, c, src)
	}
}

// ---------------------------------------------------------------------------
// Status computation
// ---------------------------------------------------------------------------

// computeOverallStatus determines the final lookup run status based on source results.
//
//   - All applicable sources succeeded → complete
//   - Some applicable sources succeeded, some failed → partial
//   - All applicable sources failed → failed
//   - No applicable sources → complete
func computeOverallStatus(sources []model.LookupSource) string {
	var applicable, succeeded, failed int
	for _, s := range sources {
		if s.Status == model.SourceStatusNotApplicable {
			continue
		}
		applicable++
		switch s.Status {
		case model.SourceStatusSuccess:
			succeeded++
		case model.SourceStatusFailed:
			failed++
		}
	}

	if applicable == 0 || succeeded == applicable {
		return model.StatusComplete
	}
	if failed == applicable {
		return model.StatusFailed
	}
	return model.StatusPartial
}
