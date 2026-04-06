//go:build integration

package lookup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mohsinian/integration-gateway/internal/client"
	"github.com/mohsinian/integration-gateway/internal/resilience"
	"github.com/mohsinian/integration-gateway/internal/store"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestOrchestrator(t *testing.T) (*Orchestrator, *pgxpool.Pool) {
	t.Helper()

	ctx := context.Background()
	dbURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		envOr("DB_USER", "gateway"),
		envOr("DB_PASSWORD", "gateway"),
		envOr("DB_HOST", "localhost"),
		envOr("DB_PORT", "5432"),
		envOr("DB_NAME", "integration_gateway"),
	)

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	log := slog.Default()

	lookupStore := store.NewLookupStore(pool)
	orch := NewOrchestrator(
		lookupStore,
		client.NewPropertyClient(envOr("MOCK_PROPERTY_URL", "http://localhost:9001"), log),
		client.NewCourtClient(envOr("MOCK_COURT_URL", "http://localhost:9002"), log),
		client.NewSCRAClient(envOr("MOCK_SCRA_URL", "http://localhost:9003"), log),
		resilience.NewCircuitBreaker("property_records", 5, 30*time.Second),
		resilience.NewCircuitBreaker("court_records", 5, 30*time.Second),
		resilience.NewCircuitBreaker("scra", 5, 30*time.Second),
		resilience.NewRateLimiter(2),
		log, log,
	)

	return orch, pool
}

// pollUntilDone polls GetStatus until the run is no longer pending or the
// timeout elapses. Returns the final LookupResult.
func pollUntilDone(t *testing.T, orch *Orchestrator, caseID string, timeout time.Duration) *LookupResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := orch.GetStatus(caseID)
		if err != nil {
			t.Fatalf("get status %s: %v", caseID, err)
		}
		if result.Run != nil && result.Run.Status != "pending" {
			return result
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s (%v)", caseID, timeout)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestIntegration_AllCases(t *testing.T) {
	orch, pool := newTestOrchestrator(t)
	defer pool.Close()

	tests := []struct {
		name         string
		caseID       string
		wantOverall  string
		wantProperty string
		wantCourt    string
		wantSCRA     string
	}{
		{"case-001_title_search", "case-001", "complete", "success", "not_applicable", "success"},
		{"case-002_all_sources", "case-002", "complete", "success", "success", "success"},
		{"case-003_all_sources", "case-003", "complete", "success", "success", "success"},
		{"case-004_active_duty", "case-004", "complete", "success", "success", "success"},
		{"case-005_title_search", "case-005", "complete", "success", "not_applicable", "success"},
		{"case-006_property_404", "case-006", "partial", "failed", "success", "success"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Trigger (idempotent — 200 if already complete, 202 if new/retry).
			result, code, err := orch.TriggerLookup(tt.caseID)
			if err != nil {
				t.Fatalf("trigger: %v", err)
			}
			if code != 200 && code != 202 {
				t.Fatalf("unexpected status code %d", code)
			}

			// If still in progress, poll until done.
			if code == 202 {
				result = pollUntilDone(t, orch, tt.caseID, 90*time.Second)
			}

			// Assert overall status.
			if result.Run.Status != tt.wantOverall {
				t.Errorf("overall status: got %q, want %q", result.Run.Status, tt.wantOverall)
			}

			// Assert per-source statuses.
			srcMap := make(map[string]string, len(result.Sources))
			for _, s := range result.Sources {
				srcMap[s.Source] = s.Status
			}
			if got := srcMap["property_records"]; got != tt.wantProperty {
				t.Errorf("property_records: got %q, want %q", got, tt.wantProperty)
			}
			if got := srcMap["court_records"]; got != tt.wantCourt {
				t.Errorf("court_records: got %q, want %q", got, tt.wantCourt)
			}
			if got := srcMap["scra"]; got != tt.wantSCRA {
				t.Errorf("scra: got %q, want %q", got, tt.wantSCRA)
			}
		})
	}
}

func TestIntegration_Idempotency(t *testing.T) {
	orch, pool := newTestOrchestrator(t)
	defer pool.Close()

	// case-002 should be complete from the previous test (or manual run).
	result, _, err := orch.TriggerLookup("case-002")
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	runID := result.Run.ID

	// Wait if still processing.
	if result.Run.Status == "pending" {
		result = pollUntilDone(t, orch, "case-002", 90*time.Second)
	}

	// Re-trigger — must return 200 with the same run ID.
	result2, code, err := orch.TriggerLookup("case-002")
	if err != nil {
		t.Fatalf("re-trigger: %v", err)
	}
	if code != 200 {
		t.Errorf("expected 200, got %d", code)
	}
	if result2.Run.ID != runID {
		t.Errorf("run ID changed: %s → %s", runID, result2.Run.ID)
	}
	if result2.Run.Status != "complete" {
		t.Errorf("status after re-trigger: %s", result2.Run.Status)
	}
}

func TestIntegration_NonexistentCase(t *testing.T) {
	orch, pool := newTestOrchestrator(t)
	defer pool.Close()

	_, _, err := orch.TriggerLookup("case-999")
	if err == nil {
		t.Fatal("expected error for nonexistent case")
	}
}

func TestIntegration_BulkEnrich(t *testing.T) {
	orch, pool := newTestOrchestrator(t)
	defer pool.Close()

	caseIDs := []string{"case-001", "case-002", "case-003", "case-004", "case-005", "case-006"}

	// Trigger all cases concurrently (simulates the bulk worker pool).
	type triggerResult struct {
		caseID string
		code   int
		err    error
	}
	resultsCh := make(chan triggerResult, len(caseIDs))

	for _, id := range caseIDs {
		go func(cid string) {
			_, code, err := orch.TriggerLookup(cid)
			resultsCh <- triggerResult{cid, code, err}
		}(id)
	}

	// Collect all trigger results.
	for range caseIDs {
		tr := <-resultsCh
		if tr.err != nil {
			t.Errorf("trigger %s: %v", tr.caseID, tr.err)
		}
		if tr.code != 200 && tr.code != 202 {
			t.Errorf("trigger %s: unexpected code %d", tr.caseID, tr.code)
		}
	}

	// Poll until all cases are done.
	expected := map[string]string{
		"case-001": "complete",
		"case-002": "complete",
		"case-003": "complete",
		"case-004": "complete",
		"case-005": "complete",
		"case-006": "partial",
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range caseIDs {
			result, err := orch.GetStatus(id)
			if err != nil {
				t.Fatalf("get status %s: %v", id, err)
			}
			if result.Run == nil || result.Run.Status == "pending" {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Assert final statuses.
	for _, id := range caseIDs {
		result, err := orch.GetStatus(id)
		if err != nil {
			t.Fatalf("final status %s: %v", id, err)
		}
		if result.Run == nil {
			t.Fatalf("no run for %s", id)
		}
		if result.Run.Status != expected[id] {
			t.Errorf("%s: got %q, want %q", id, result.Run.Status, expected[id])
		}
	}
}

func TestIntegration_PartialRetry(t *testing.T) {
	orch, pool := newTestOrchestrator(t)
	defer pool.Close()

	// case-006 should be partial (property 404 is permanent).
	// Re-triggering should return 202 (retrying failed sources).
	_, _, err := orch.TriggerLookup("case-006")
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	// Wait for it to settle.
	result := pollUntilDone(t, orch, "case-006", 90*time.Second)

	// Property must still be failed (404 is permanent — retry won't help).
	srcMap := make(map[string]string, len(result.Sources))
	for _, s := range result.Sources {
		srcMap[s.Source] = s.Status
	}
	if srcMap["property_records"] != "failed" {
		t.Errorf("property_records: got %q, want %q", srcMap["property_records"], "failed")
	}
	if srcMap["court_records"] != "success" {
		t.Errorf("court_records: got %q, want %q", srcMap["court_records"], "success")
	}
	if srcMap["scra"] != "success" {
		t.Errorf("scra: got %q, want %q", srcMap["scra"], "success")
	}
	// Overall should remain partial.
	if result.Run.Status != "partial" {
		t.Errorf("overall: got %q, want %q", result.Run.Status, "partial")
	}
}
