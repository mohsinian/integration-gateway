package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// SCRA request/response types — mirror the mock service JSON shapes.
// ---------------------------------------------------------------------------

// SCRASearchRequest is the JSON body sent to the SCRA search endpoint.
type SCRASearchRequest struct {
	LastName  string `json:"lastName"`
	FirstName string `json:"firstName"`
	SSNLast4  string `json:"ssnLast4"`
	DOB       string `json:"dob"`
}

// SCRASubmitResponse is the JSON response from POST /api/scra/search (202).
type SCRASubmitResponse struct {
	SearchID                   string `json:"searchId"`
	Status                     string `json:"status"`
	SubmittedAt                string `json:"submittedAt"`
	EstimatedCompletionSeconds int    `json:"estimatedCompletionSeconds"`
}

// SCRAResult represents the military status data returned by the SCRA service.
type SCRAResult struct {
	ActiveDuty     bool    `json:"activeDuty"`
	LastName       string  `json:"lastName"`
	FirstName      string  `json:"firstName"`
	SearchDate     string  `json:"searchDate"`
	CertificateURL *string `json:"certificateUrl"`
}

// SCRAPollResponse is the JSON response from GET /api/scra/results/{searchId}.
// The shape varies by status, so all optional fields are pointers.
type SCRAPollResponse struct {
	SearchID    string      `json:"searchId"`
	Status      string      `json:"status"` // "pending", "complete", "error"
	Result      *SCRAResult `json:"result,omitempty"`
	Error       string      `json:"error,omitempty"`
	CompletedAt string      `json:"completedAt,omitempty"`
}

// ---------------------------------------------------------------------------
// SCRAClient
// ---------------------------------------------------------------------------

// 5 seconds per call — the poll itself is fast; the waiting is done by orchestration.
const scraHTTPTimeout = 5 * time.Second

// SCRAClient talks to the SCRA Military Status mock service.
type SCRAClient struct {
	httpClient *http.Client
	baseURL    string
	log        *slog.Logger
}

// NewSCRAClient creates a client for the SCRA service.
// baseURL should be like "http://mock-services:9003".
func NewSCRAClient(baseURL string, log *slog.Logger) *SCRAClient {
	return &SCRAClient{
		httpClient: &http.Client{Timeout: scraHTTPTimeout},
		baseURL:    strings.TrimRight(baseURL, "/"),
		log:        log,
	}
}

// SubmitSearch posts a new SCRA search request. On success it returns the
// searchID that should be used for subsequent polling.
//
// Returns typed errors:
//   - ErrTimeout — request timed out, retryable
func (c *SCRAClient) SubmitSearch(ctx context.Context, req SCRASearchRequest) (string, error) {
	url := c.baseURL + "/api/scra/search"

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	c.log.Info("submitting SCRA search",
		slog.String("lastName", req.LastName),
		slog.String("firstName", req.FirstName),
	)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if isTimeout(ctx, err) {
			return "", fmt.Errorf("%w: %s", ErrTimeout, err)
		}
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("unexpected status %d from scra search: %s", resp.StatusCode, string(respBody))
	}

	var submitResp SCRASubmitResponse
	if err := json.Unmarshal(respBody, &submitResp); err != nil {
		return "", fmt.Errorf("decode submit response: %w", err)
	}

	c.log.Info("SCRA search submitted",
		slog.String("searchId", submitResp.SearchID),
		slog.Int("estimatedSeconds", submitResp.EstimatedCompletionSeconds),
	)

	return submitResp.SearchID, nil
}

// PollResult fetches the current status of an SCRA search.
//
// Returns:
//   - (nil, "pending", nil)   — search still in progress
//   - (result, "complete", nil) — search finished successfully
//   - (nil, "error", errMsg)  — search permanently failed
//   - (nil, "", err)          — transport / protocol error
func (c *SCRAClient) PollResult(ctx context.Context, searchID string) (*SCRAResult, string, error) {
	url := c.baseURL + "/api/scra/results/" + searchID

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, "", fmt.Errorf("%w: %s", ErrTimeout, err)
		}
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusAccepted:
		// Still pending (202).
		return nil, "pending", nil

	case http.StatusOK:
		var pollResp SCRAPollResponse
		if err := json.Unmarshal(respBody, &pollResp); err != nil {
			return nil, "", fmt.Errorf("decode poll response: %w", err)
		}

		switch pollResp.Status {
		case "complete":
			c.log.Info("SCRA result ready",
				slog.String("searchId", searchID),
				slog.Bool("activeDuty", pollResp.Result.ActiveDuty),
			)
			return pollResp.Result, "complete", nil

		case "error":
			c.log.Warn("SCRA search failed",
				slog.String("searchId", searchID),
				slog.String("error", pollResp.Error),
			)
			return nil, "error", fmt.Errorf("%w: %s", ErrSearchFailed, pollResp.Error)

		default:
			return nil, "", fmt.Errorf("unexpected poll status %q", pollResp.Status)
		}

	case http.StatusNotFound:
		// Unknown searchId — treat as permanent.
		return nil, "", fmt.Errorf("%w: searchId %s not found", ErrNotFound, searchID)

	default:
		return nil, "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
}
