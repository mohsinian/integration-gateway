package client

import (
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
// Property Record types — mirror the mock service JSON response.
// ---------------------------------------------------------------------------

type PropertyRecord struct {
	ParcelID         string     `json:"parcelId"`
	County           string     `json:"county"`
	State            string     `json:"state"`
	Address          string     `json:"address"`
	Owner            OwnerInfo  `json:"owner"`
	Liens            []LienInfo `json:"liens"`
	TaxStatus        TaxInfo    `json:"taxStatus"`
	LegalDescription string     `json:"legalDescription"`
	Easements        []string   `json:"easements"`
	LastUpdated      string     `json:"lastUpdated"`
}

type OwnerInfo struct {
	Name        string `json:"name"`
	VestingType string `json:"vestingType"`
	DeedDate    string `json:"deedDate"`
	DeedType    string `json:"deedType"`
	Instrument  string `json:"instrument"`
}

type LienInfo struct {
	Position     int     `json:"position"`
	Type         string  `json:"type"`
	Holder       string  `json:"holder"`
	Amount       float64 `json:"amount"`
	RecordedDate string  `json:"recordedDate"`
	Instrument   string  `json:"instrument"`
	Status       string  `json:"status"`
}

type TaxInfo struct {
	Year         int     `json:"year"`
	Status       string  `json:"status"`
	Amount       float64 `json:"amount"`
	ParcelNumber string  `json:"parcelNumber"`
}

// ---------------------------------------------------------------------------
// PropertyClient
// ---------------------------------------------------------------------------

// 12 seconds accommodates the mock service's 8-second slow responses plus buffer.
const propertyHTTPTimeout = 12 * time.Second

// PropertyClient fetches property records from the external mock service.
type PropertyClient struct {
	httpClient *http.Client
	baseURL    string
	log        *slog.Logger
}

// NewPropertyClient creates a client for the Property Records service.
// baseURL should be like "http://mock-services:9001".
func NewPropertyClient(baseURL string, log *slog.Logger) *PropertyClient {
	return &PropertyClient{
		httpClient: &http.Client{Timeout: propertyHTTPTimeout},
		baseURL:    strings.TrimRight(baseURL, "/"),
		log:        log,
	}
}

// Fetch retrieves property records for the given state, county, and parcel ID.
//
// Returns typed errors for the caller to decide retry strategy:
//   - ErrNotFound (404)           → permanent, do not retry
//   - ErrServiceUnavailable (503) → transient, retry
//   - ErrTimeout                  → transient, retry
func (c *PropertyClient) Fetch(ctx context.Context, state, county, parcelID string) (*PropertyRecord, error) {
	url := fmt.Sprintf("%s/api/properties/%s/%s/%s",
		c.baseURL, state, county, parcelID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	c.log.Info("fetching property records",
		slog.String("state", state),
		slog.String("county", county),
		slog.String("parcelId", parcelID),
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, fmt.Errorf("%w: %s", ErrTimeout, err)
		}
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var record PropertyRecord
		if err := json.Unmarshal(body, &record); err != nil {
			return nil, fmt.Errorf("decode json: %w", err)
		}
		c.log.Info("property records fetched",
			slog.String("parcelId", record.ParcelID),
			slog.Int("liens", len(record.Liens)),
		)
		return &record, nil

	case http.StatusNotFound:
		c.log.Warn("property not found",
			slog.String("state", state),
			slog.String("county", county),
			slog.String("parcelId", parcelID),
		)
		return nil, fmt.Errorf("%w: %s/%s/%s", ErrNotFound, state, county, parcelID)

	case http.StatusServiceUnavailable:
		c.log.Warn("property service unavailable",
			slog.String("status", "503"),
			slog.String("state", state),
			slog.String("county", county),
		)
		return nil, ErrServiceUnavailable

	default:
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}
