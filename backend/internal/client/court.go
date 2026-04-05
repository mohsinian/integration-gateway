package client

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Court Record types — mirror the mock service XML response.
// ---------------------------------------------------------------------------

type CourtRecord struct {
	XMLName     xml.Name     `xml:"CourtRecordResponse"`
	CaseNumber  string       `xml:"CaseNumber"`
	Court       string       `xml:"Court"`
	Division    string       `xml:"Division"`
	Judge       string       `xml:"Judge"`
	FilingDate  string       `xml:"FilingDate"`
	CaseType    string       `xml:"CaseType"`
	Status      string       `xml:"Status"`
	Message     string       `xml:"Message,omitempty"`
	Parties     *Parties     `xml:"Parties,omitempty"`
	Filings     *FilingsList `xml:"Filings,omitempty"`
	NextHearing *HearingInfo `xml:"NextHearing,omitempty"`
}

type Parties struct {
	Plaintiff string `xml:"Plaintiff"`
	Defendant string `xml:"Defendant"`
}

type FilingsList struct {
	Items []Filing `xml:"Filing"`
}

type Filing struct {
	Type           string `xml:"Type"`
	FiledDate      string `xml:"FiledDate"`
	DocumentNumber string `xml:"DocumentNumber"`
}

type HearingInfo struct {
	Date      string `xml:"Date"`
	Time      string `xml:"Time"`
	Type      string `xml:"Type"`
	Courtroom string `xml:"Courtroom"`
}

// ---------------------------------------------------------------------------
// CourtClient
// ---------------------------------------------------------------------------

// 10 seconds for court records per the plan.
const courtHTTPTimeout = 10 * time.Second

// CourtClient fetches court records from the external mock service.
type CourtClient struct {
	httpClient *http.Client
	baseURL    string
	log        *slog.Logger
}

// NewCourtClient creates a client for the Court Records service.
// baseURL should be like "http://mock-services:9002".
func NewCourtClient(baseURL string, log *slog.Logger) *CourtClient {
	return &CourtClient{
		httpClient: &http.Client{Timeout: courtHTTPTimeout},
		baseURL:    strings.TrimRight(baseURL, "/"),
		log:        log,
	}
}

// courtSearchRequest is the JSON body sent to the court records service.
type courtSearchRequest struct {
	CaseNumber string `json:"caseNumber"`
}

// Search queries court records for the given case number.
//
// Returns:
//   - (*CourtRecord, nil) — records found
//   - (nil, nil) — no filing found (NoFilingFound status), not an error
//   - (nil, ErrRateLimited) — 429 response, retryable
//   - (nil, ErrMalformedResponse) — bad XML, retryable
//   - (nil, ErrTimeout) — request timed out, retryable
func (c *CourtClient) Search(ctx context.Context, caseNumber string) (*CourtRecord, error) {
	url := c.baseURL + "/api/court-records/search"

	body, err := json.Marshal(courtSearchRequest{CaseNumber: caseNumber})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	c.log.Info("searching court records",
		slog.String("caseNumber", caseNumber),
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, fmt.Errorf("%w: %s", ErrTimeout, err)
		}
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var record CourtRecord
		if err := xml.Unmarshal(respBody, &record); err != nil {
			c.log.Warn("failed to parse court records XML",
				slog.String("caseNumber", caseNumber),
				slog.String("error", err.Error()),
			)
			return nil, fmt.Errorf("%w: %s", ErrMalformedResponse, err)
		}

		// NoFilingFound is a valid outcome — the case exists but has no court filings.
		if record.Status == "NoFilingFound" {
			c.log.Info("no court filing found",
				slog.String("caseNumber", caseNumber),
			)
			return nil, nil
		}

		c.log.Info("court records fetched",
			slog.String("caseNumber", record.CaseNumber),
			slog.String("court", record.Court),
		)
		return &record, nil

	case http.StatusTooManyRequests:
		retryAfter := resp.Header.Get("Retry-After")
		c.log.Warn("court service rate limited",
			slog.String("caseNumber", caseNumber),
			slog.String("retryAfter", retryAfter),
		)
		return nil, ErrRateLimited

	default:
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
}
