package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mohsinian/integration-gateway/internal/logger"
	"github.com/mohsinian/integration-gateway/internal/lookup"
	"github.com/mohsinian/integration-gateway/internal/resilience"
	"github.com/mohsinian/integration-gateway/internal/store"
)

// Handler holds dependencies for all HTTP endpoints.
type Handler struct {
	orchestrator *lookup.Orchestrator
	store        *store.LookupStore
	pool         *pgxpool.Pool
	breakers     []*resilience.CircuitBreaker
	logs         *logger.Loggers
}

// NewHandler creates a Handler with all required dependencies.
func NewHandler(
	orchestrator *lookup.Orchestrator,
	store *store.LookupStore,
	pool *pgxpool.Pool,
	breakers []*resilience.CircuitBreaker,
	logs *logger.Loggers,
) *Handler {
	return &Handler{
		orchestrator: orchestrator,
		store:        store,
		pool:         pool,
		breakers:     breakers,
		logs:         logs,
	}
}

// RegisterRoutes registers all API endpoints on the given mux using Go 1.22+ patterns.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/cases/{id}/enrich", h.LookupCase)
	mux.HandleFunc("POST /api/enrich/bulk", h.BulkEnrich)
	mux.HandleFunc("GET /api/cases/{id}/enrichment", h.GetLookupStatus)
	mux.HandleFunc("GET /api/cases", h.ListCases)
	mux.HandleFunc("GET /api/health", h.Health)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// LookupCase handles POST /api/cases/{id}/enrich.
func (h *Handler) LookupCase(w http.ResponseWriter, r *http.Request) {
	caseID := r.PathValue("id")
	if caseID == "" {
		h.writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing case id"})
		return
	}

	result, statusCode, err := h.orchestrator.TriggerLookup(caseID)
	if err != nil {
		h.logs.Error.Error("lookup trigger failed",
			slog.String("caseId", caseID),
			slog.String("error", err.Error()),
		)
		if strings.Contains(err.Error(), "not found") {
			h.writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	h.logs.Server.Info("lookup triggered",
		slog.String("caseId", caseID),
		slog.Int("status", statusCode),
	)

	h.writeJSON(w, statusCode, buildStatusResponse(result))
}

// GetLookupStatus handles GET /api/cases/{id}/enrichment.
func (h *Handler) GetLookupStatus(w http.ResponseWriter, r *http.Request) {
	caseID := r.PathValue("id")
	if caseID == "" {
		h.writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing case id"})
		return
	}

	result, err := h.orchestrator.GetStatus(caseID)
	if err != nil {
		h.logs.Error.Error("get status failed",
			slog.String("caseId", caseID),
			slog.String("error", err.Error()),
		)
		if strings.Contains(err.Error(), "not found") {
			h.writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	if result.Run == nil {
		h.writeJSON(w, http.StatusOK, map[string]any{
			"caseId":      caseID,
			"status":      "not_started",
			"sources":     nil,
			"startedAt":   nil,
			"completedAt": nil,
		})
		return
	}

	h.writeJSON(w, http.StatusOK, buildStatusResponse(result))
}

// ListCases handles GET /api/cases.
func (h *Handler) ListCases(w http.ResponseWriter, r *http.Request) {
	cases, err := h.store.ListCases(r.Context())
	if err != nil {
		h.logs.Error.Error("list cases failed", slog.String("error", err.Error()))
		h.writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	type caseSummary struct {
		ID           string  `json:"id"`
		CaseNumber   string  `json:"caseNumber"`
		Borrower     string  `json:"borrower"`
		Address      string  `json:"address"`
		CurrentStage string  `json:"currentStage"`
		LookupStatus *string `json:"lookupStatus,omitempty"`
	}

	summaries := make([]caseSummary, 0, len(cases))
	for _, c := range cases {
		s := caseSummary{
			ID:           c.ID,
			CaseNumber:   c.CaseNumber,
			Borrower:     c.LastName + ", " + c.FirstName,
			Address:      c.Address + ", " + c.County + ", " + c.State,
			CurrentStage: c.CurrentStage,
		}
		run, err := h.store.GetLookupRunByCaseID(r.Context(), c.ID)
		if err == nil && run != nil {
			s.LookupStatus = &run.Status
		}
		summaries = append(summaries, s)
	}

	h.writeJSON(w, http.StatusOK, summaries)
}

// Health handles GET /api/health.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	dbStatus := "up"
	if err := h.pool.Ping(r.Context()); err != nil {
		h.logs.Error.Error("health check failed — db down",
			slog.String("remote", r.RemoteAddr),
			slog.String("error", err.Error()),
		)
		dbStatus = "down"
	}

	cbStatus := make(map[string]cbInfo)
	for _, cb := range h.breakers {
		cbStatus[cb.Name()] = cbInfo{
			State:    string(cb.State()),
			Failures: cb.Failures(),
		}
	}

	overall := "healthy"
	if dbStatus == "down" {
		overall = "unhealthy"
	}

	h.logs.Server.Info("health check", slog.String("remote", r.RemoteAddr))

	code := http.StatusOK
	if overall == "unhealthy" {
		code = http.StatusServiceUnavailable
	}

	h.writeJSON(w, code, healthResponse{
		Status:          overall,
		DB:              dbStatus,
		CircuitBreakers: cbStatus,
	})
}

// BulkEnrich handles POST /api/enrich/bulk.
// Accepts {"caseIds": ["case-001", "case-002", ...]}, triggers enrichment for each
// with a worker pool of 3 (matching the number of sources to respect the court rate limiter).
// Returns 202 with per-case status immediately — enrichment is async.
func (h *Handler) BulkEnrich(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CaseIDs []string `json:"caseIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}
	if len(req.CaseIDs) == 0 {
		h.writeJSON(w, http.StatusBadRequest, errorResponse{Error: "caseIds must not be empty"})
		return
	}

	type caseResult struct {
		CaseID string `json:"caseId"`
		Status string `json:"status"` // "triggered", "complete", "pending", "error"
		Error  string `json:"error,omitempty"`
	}

	results := make([]caseResult, len(req.CaseIDs))
	var mu sync.Mutex

	// Worker pool: 3 concurrent lookups (court rate limiter is 2/sec, so 3 workers
	// keeps throughput up without overwhelming it).
	workers := 3
	if len(req.CaseIDs) < workers {
		workers = len(req.CaseIDs)
	}

	jobs := make(chan int, len(req.CaseIDs))
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				caseID := req.CaseIDs[idx]
				_, code, err := h.orchestrator.TriggerLookup(caseID)

				mu.Lock()
				cr := caseResult{CaseID: caseID}
				if err != nil {
					cr.Status = "error"
					cr.Error = err.Error()
				} else {
					switch code {
					case 200:
						cr.Status = "complete"
					case 202:
						cr.Status = "triggered"
					}
				}
				results[idx] = cr
				mu.Unlock()
			}
		}()
	}

	for i := range req.CaseIDs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	h.logs.Server.Info("bulk enrich triggered",
		slog.Int("cases", len(req.CaseIDs)),
	)

	h.writeJSON(w, http.StatusAccepted, map[string]any{
		"triggered": len(req.CaseIDs),
		"cases":     results,
	})
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

type errorResponse struct {
	Error string `json:"error"`
}

type cbInfo struct {
	State    string `json:"state"`
	Failures int    `json:"failures"`
}

type healthResponse struct {
	Status          string         `json:"status"`
	DB              string         `json:"db"`
	CircuitBreakers map[string]cbInfo `json:"circuitBreakers"`
}

type sourceResponse struct {
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts,omitempty"`
	LastAttempt *string         `json:"lastAttempt,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	SearchID    string          `json:"searchId,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type statusResponse struct {
	CaseID      string                    `json:"caseId"`
	RunID       string                    `json:"runId,omitempty"`
	Status      string                    `json:"status"`
	Sources     map[string]sourceResponse `json:"sources"`
	StartedAt   *string                   `json:"startedAt,omitempty"`
	CompletedAt *string                   `json:"completedAt,omitempty"`
}

func buildStatusResponse(result *lookup.LookupResult) statusResponse {
	resp := statusResponse{
		CaseID:  result.Case.ID,
		Sources: make(map[string]sourceResponse),
	}

	if result.Run != nil {
		resp.RunID = result.Run.ID
		resp.Status = result.Run.Status
		t := result.Run.StartedAt.Format(time.RFC3339)
		resp.StartedAt = &t
		if result.Run.CompletedAt != nil {
			ct := result.Run.CompletedAt.Format(time.RFC3339)
			resp.CompletedAt = &ct
		}
	}

	for _, src := range result.Sources {
		sr := sourceResponse{
			Status:   src.Status,
			Attempts: src.Attempts,
		}
		if src.LastAttemptAt != nil {
			la := src.LastAttemptAt.Format(time.RFC3339)
			sr.LastAttempt = &la
		}
		if src.Data != nil {
			sr.Data = src.Data
		}
		if src.Reason != nil {
			sr.Reason = *src.Reason
		}
		if src.SearchID != nil {
			sr.SearchID = *src.SearchID
		}
		if src.ErrorMessage != nil {
			sr.Error = *src.ErrorMessage
		}
		resp.Sources[src.Source] = sr
	}

	return resp
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
