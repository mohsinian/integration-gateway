package client

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// PropertyClient tests
// ---------------------------------------------------------------------------

func TestPropertyClient_Fetch_Success(t *testing.T) {
	expected := PropertyRecord{
		ParcelID: "14-28-322-001-0000",
		County:   "Cook",
		State:    "IL",
		Address:  "1422 W Diversey Pkwy, Chicago, IL 60614",
		Owner: OwnerInfo{
			Name:        "Elena Martinez",
			VestingType: "Fee Simple",
			DeedDate:    "2019-06-15",
			DeedType:    "Warranty Deed",
			Instrument:  "2019R0567890",
		},
		Liens: []LienInfo{
			{Position: 1, Type: "Mortgage", Holder: "JPMorgan Chase Bank, N.A.", Amount: 385000, Status: "Active"},
		},
		TaxStatus: TaxInfo{Year: 2025, Status: "Current", Amount: 6240, ParcelNumber: "14-28-322-001-0000"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expected)
	}))
	defer srv.Close()

	c := NewPropertyClient(srv.URL, slog.Default())
	rec, err := c.Fetch(context.Background(), "IL", "Cook", "14-28-322-001-0000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.ParcelID != expected.ParcelID {
		t.Fatalf("expected parcelId %q, got %q", expected.ParcelID, rec.ParcelID)
	}
	if len(rec.Liens) != 1 {
		t.Fatalf("expected 1 lien, got %d", len(rec.Liens))
	}
}

func TestPropertyClient_Fetch_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewPropertyClient(srv.URL, slog.Default())
	_, err := c.Fetch(context.Background(), "TX", "Harris", "no-exist")
	if !IsPermanent(err) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	// ErrNotFound is a *PermanentError wrapping "not found".
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestPropertyClient_Fetch_ServiceUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewPropertyClient(srv.URL, slog.Default())
	_, err := c.Fetch(context.Background(), "IL", "Cook", "abc")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// 503 should be retryable, not permanent.
	if IsPermanent(err) {
		t.Fatal("503 should not be permanent")
	}
}

func TestPropertyClient_Fetch_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewPropertyClient(srv.URL, slog.Default())
	// Use a context that expires before the server responds.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Fetch(ctx, "IL", "Cook", "slow")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// ---------------------------------------------------------------------------
// CourtClient tests
// ---------------------------------------------------------------------------

const validCourtXML = `<?xml version="1.0" encoding="UTF-8"?>
<CourtRecordResponse>
  <CaseNumber>2026-CA-003891</CaseNumber>
  <Court>Circuit Court of Miami-Dade County</Court>
  <Division>Civil</Division>
  <Judge>Hon. Patricia Navarro</Judge>
  <FilingDate>2025-11-20</FilingDate>
  <CaseType>Foreclosure</CaseType>
  <Status>Active</Status>
  <Parties>
    <Plaintiff>Wells Fargo Bank, N.A.</Plaintiff>
    <Defendant>David R. Thompson</Defendant>
  </Parties>
  <Filings>
    <Filing>
      <Type>Complaint</Type>
      <FiledDate>2025-11-20</FiledDate>
      <DocumentNumber>2025-CI-28934</DocumentNumber>
    </Filing>
  </Filings>
  <NextHearing>
    <Date>2026-04-22</Date>
    <Time>10:00</Time>
    <Type>Case Management Conference</Type>
    <Courtroom>5-3</Courtroom>
  </NextHearing>
</CourtRecordResponse>`

func TestCourtClient_Search_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(validCourtXML))
	}))
	defer srv.Close()

	c := NewCourtClient(srv.URL, slog.Default())
	rec, err := c.Search(context.Background(), "2026-CA-003891")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.CaseNumber != "2026-CA-003891" {
		t.Fatalf("expected case number, got %q", rec.CaseNumber)
	}
	if rec.Judge != "Hon. Patricia Navarro" {
		t.Fatalf("expected judge name, got %q", rec.Judge)
	}
}

func TestCourtClient_Search_NoFilingFound(t *testing.T) {
	noFilingXML := `<?xml version="1.0" encoding="UTF-8"?>
<CourtRecordResponse>
  <CaseNumber>REQUESTED-NUMBER</CaseNumber>
  <Status>NoFilingFound</Status>
  <Message>No court filings found.</Message>
</CourtRecordResponse>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(noFilingXML))
	}))
	defer srv.Close()

	c := NewCourtClient(srv.URL, slog.Default())
	rec, err := c.Search(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("NoFilingFound should not return error, got %v", err)
	}
	if rec != nil {
		t.Fatal("NoFilingFound should return nil record")
	}
}

func TestCourtClient_Search_MalformedXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte("<CourtRecordResponse><CaseNumber>broken")) // truncated
	}))
	defer srv.Close()

	c := NewCourtClient(srv.URL, slog.Default())
	_, err := c.Search(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error for malformed XML")
	}
}

func TestCourtClient_Search_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewCourtClient(srv.URL, slog.Default())
	_, err := c.Search(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error for 429")
	}
	// ErrRateLimited is not permanent — it's retryable.
	if IsPermanent(err) {
		t.Fatal("429 should not be permanent")
	}
}

// ---------------------------------------------------------------------------
// SCRAClient tests
// ---------------------------------------------------------------------------

func TestSCRAClient_SubmitSearch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		resp := SCRASubmitResponse{
			SearchID:                   "scra-123",
			Status:                     "pending",
			EstimatedCompletionSeconds: 3,
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewSCRAClient(srv.URL, slog.Default())
	searchID, err := c.SubmitSearch(context.Background(), SCRASearchRequest{
		LastName: "Martinez", FirstName: "Elena", SSNLast4: "4521", DOB: "1985-03-14",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if searchID != "scra-123" {
		t.Fatalf("expected 'scra-123', got %q", searchID)
	}
}

func TestSCRAClient_PollResult_Pending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(SCRAPollResponse{SearchID: "scra-123", Status: "pending"})
	}))
	defer srv.Close()

	c := NewSCRAClient(srv.URL, slog.Default())
	result, status, err := c.PollResult(context.Background(), "scra-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "pending" {
		t.Fatalf("expected 'pending', got %q", status)
	}
	if result != nil {
		t.Fatal("expected nil result for pending status")
	}
}

func TestSCRAClient_PollResult_Complete(t *testing.T) {
	active := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SCRAPollResponse{
			SearchID: "scra-123",
			Status:   "complete",
			Result:   &SCRAResult{ActiveDuty: active, LastName: "Martinez", FirstName: "Elena"},
		})
	}))
	defer srv.Close()

	c := NewSCRAClient(srv.URL, slog.Default())
	result, status, err := c.PollResult(context.Background(), "scra-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "complete" {
		t.Fatalf("expected 'complete', got %q", status)
	}
	if result.ActiveDuty {
		t.Fatal("expected activeDuty=false")
	}
}

func TestSCRAClient_PollResult_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SCRAPollResponse{
			SearchID: "scra-123",
			Status:   "error",
			Error:    "unable to verify military status",
		})
	}))
	defer srv.Close()

	c := NewSCRAClient(srv.URL, slog.Default())
	_, status, err := c.PollResult(context.Background(), "scra-123")
	if err == nil {
		t.Fatal("expected error for failed search")
	}
	if status != "error" {
		t.Fatalf("expected 'error' status, got %q", status)
	}
}

func TestSCRAClient_PollResult_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewSCRAClient(srv.URL, slog.Default())
	_, _, err := c.PollResult(context.Background(), "bad-id")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !IsPermanent(err) {
		t.Fatal("404 should be permanent")
	}
}

// Verify XML round-trip works at init time.
func init() {
	var rec CourtRecord
	if err := xml.Unmarshal([]byte(validCourtXML), &rec); err != nil {
		panic("test setup: invalid court XML: " + err.Error())
	}
}
