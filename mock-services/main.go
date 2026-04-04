// Mock External Services for Backend Engineer Assignment
//
// Run:  go run main.go
//
// Starts three services:
//   Port 9001 — Property Records API   (REST/JSON, intermittent 503s, occasional slow responses)
//   Port 9002 — Court Records System    (JSON in → XML out, rate-limited 2 req/sec, occasional malformed XML)
//   Port 9003 — SCRA Military Status    (async: submit search → poll for results)
//
// Environment variables (optional):
//   PROPERTY_FAIL_RATE    — Probability of 503 (default: 0.25)
//   PROPERTY_SLOW_RATE    — Probability of 8s delay (default: 0.10)
//   COURT_RATE_LIMIT      — Max requests per second (default: 2)
//   COURT_CORRUPT_RATE    — Probability of malformed XML (default: 0.03)
//   SCRA_FAIL_RATE        — Probability of search failure (default: 0.10)
//   SCRA_MIN_DELAY        — Minimum result delay in seconds (default: 2)
//   SCRA_MAX_DELAY        — Maximum result delay in seconds (default: 5)

package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================
// Configuration
// ============================================================

type config struct {
	PropertyFailRate  float64
	PropertySlowRate  float64
	CourtRateLimit    int
	CourtCorruptRate  float64
	SCRAFailRate      float64
	SCRAMinDelay      int
	SCRAMaxDelay      int
}

func loadConfig() config {
	return config{
		PropertyFailRate:  envFloat("PROPERTY_FAIL_RATE", 0.25),
		PropertySlowRate:  envFloat("PROPERTY_SLOW_RATE", 0.10),
		CourtRateLimit:    envInt("COURT_RATE_LIMIT", 2),
		CourtCorruptRate:  envFloat("COURT_CORRUPT_RATE", 0.03),
		SCRAFailRate:      envFloat("SCRA_FAIL_RATE", 0.10),
		SCRAMinDelay:      envInt("SCRA_MIN_DELAY", 2),
		SCRAMaxDelay:      envInt("SCRA_MAX_DELAY", 5),
	}
}

// ============================================================
// Data Types
// ============================================================

// --- Property Records ---

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

// --- Court Records (XML) ---

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

// --- SCRA ---

type SCRAResult struct {
	ActiveDuty     bool    `json:"activeDuty"`
	LastName       string  `json:"lastName"`
	FirstName      string  `json:"firstName"`
	SearchDate     string  `json:"searchDate"`
	CertificateURL *string `json:"certificateUrl"`
}

type scraSearch struct {
	mu          sync.Mutex
	SearchID    string
	Status      string // "pending", "complete", "error"
	Result      *SCRAResult
	Error       string
	SubmittedAt time.Time
	ReadyAt     time.Time
}

// ============================================================
// Mock Data — Property Records
// ============================================================

var propertyData = map[string]*PropertyRecord{
	// Key format: "state:county:parcelid" (all lowercase)
	"il:cook:14-28-322-001-0000": {
		ParcelID: "14-28-322-001-0000", County: "Cook", State: "IL",
		Address: "1422 W Diversey Pkwy, Chicago, IL 60614",
		Owner: OwnerInfo{
			Name: "Elena Martinez", VestingType: "Fee Simple",
			DeedDate: "2019-06-15", DeedType: "Warranty Deed", Instrument: "2019R0567890",
		},
		Liens: []LienInfo{
			{Position: 1, Type: "Mortgage", Holder: "JPMorgan Chase Bank, N.A.", Amount: 385000.00, RecordedDate: "2019-06-15", Instrument: "2019R0567891", Status: "Active"},
			{Position: 2, Type: "HOA Lien", Holder: "Diversey Park HOA", Amount: 2150.00, RecordedDate: "2025-11-03", Instrument: "2025R0892341", Status: "Active"},
		},
		TaxStatus:        TaxInfo{Year: 2025, Status: "Current", Amount: 6240.00, ParcelNumber: "14-28-322-001-0000"},
		LegalDescription: "LOT 14 IN BLOCK 3 OF SHEFFIELD'S ADDITION TO CHICAGO IN THE SE 1/4 OF SECTION 28, TOWNSHIP 40 NORTH, RANGE 14 EAST OF THE THIRD PRINCIPAL MERIDIAN, COOK COUNTY, ILLINOIS",
		Easements:        []string{"ComEd utility easement (Book 12045, Page 223)"},
		LastUpdated:      "2026-03-15T14:30:00Z",
	},

	"fl:miami-dade:30-4017-001-0290": {
		ParcelID: "30-4017-001-0290", County: "Miami-Dade", State: "FL",
		Address: "8830 SW 142nd St, Miami, FL 33176",
		Owner: OwnerInfo{
			Name: "David R. Thompson", VestingType: "Fee Simple",
			DeedDate: "2020-03-10", DeedType: "Warranty Deed", Instrument: "CFN2020R0234567",
		},
		Liens: []LienInfo{
			{Position: 1, Type: "Mortgage", Holder: "Wells Fargo Bank, N.A.", Amount: 520000.00, RecordedDate: "2020-03-10", Instrument: "CFN2020R0234568", Status: "Active"},
			{Position: 2, Type: "Assignment of Mortgage", Holder: "Nationstar Mortgage LLC d/b/a Mr. Cooper", Amount: 520000.00, RecordedDate: "2025-09-14", Instrument: "CFN2025R0891234", Status: "Active"},
			{Position: 3, Type: "HOA Lis Pendens", Holder: "Palmetto Bay Homeowners Association, Inc.", Amount: 3420.00, RecordedDate: "2026-01-22", Instrument: "CFN2026R0034567", Status: "Active"},
		},
		TaxStatus:        TaxInfo{Year: 2025, Status: "Delinquent", Amount: 8247.00, ParcelNumber: "30-4017-001-0290"},
		LegalDescription: "LOT 29, BLOCK 1, OF PALMETTO BAY ESTATES, ACCORDING TO THE PLAT THEREOF, RECORDED IN PLAT BOOK 87, PAGE 44, OF THE PUBLIC RECORDS OF MIAMI-DADE COUNTY, FLORIDA",
		Easements:        []string{"FPL electrical transmission easement (O.R. Book 18924, Page 445)", "Drainage easement to Miami-Dade County (O.R. Book 19102, Page 112)"},
		LastUpdated:      "2026-03-12T09:15:00Z",
	},

	"tx:harris:61-42-0100-0034": {
		ParcelID: "61-42-0100-0034", County: "Harris", State: "TX",
		Address: "4501 Kingwood Dr, Kingwood, TX 77345",
		Owner: OwnerInfo{
			Name: "Thanh Nguyen and Linh Nguyen", VestingType: "Community Property",
			DeedDate: "2021-01-20", DeedType: "General Warranty Deed", Instrument: "RP-2021-045678",
		},
		Liens: []LienInfo{
			{Position: 1, Type: "Deed of Trust", Holder: "Nationstar Mortgage LLC d/b/a Mr. Cooper", Amount: 310000.00, RecordedDate: "2021-01-20", Instrument: "RP-2021-045679", Status: "Active"},
		},
		TaxStatus:        TaxInfo{Year: 2025, Status: "Current", Amount: 5890.00, ParcelNumber: "61-42-0100-0034"},
		LegalDescription: "LOT 34, BLOCK 1, OF KINGWOOD SECTION 42, A SUBDIVISION IN HARRIS COUNTY, TEXAS, ACCORDING TO THE MAP RECORDED IN VOLUME 254, PAGE 18, OF THE MAP RECORDS OF HARRIS COUNTY, TEXAS",
		Easements:        []string{"CenterPoint Energy easement (Vol. 301, Pg. 89)"},
		LastUpdated:      "2026-03-10T11:00:00Z",
	},

	"ny:kings:00945-0012-0001": {
		ParcelID: "00945-0012-0001", County: "Kings", State: "NY",
		Address: "482 Atlantic Ave, Brooklyn, NY 11217",
		Owner: OwnerInfo{
			Name: "Marcus D. Johnson", VestingType: "Fee Simple",
			DeedDate: "2022-05-12", DeedType: "Bargain and Sale Deed", Instrument: "CRFN2022000345678",
		},
		Liens: []LienInfo{
			{Position: 1, Type: "Mortgage", Holder: "JPMorgan Chase Bank, N.A.", Amount: 675000.00, RecordedDate: "2022-05-12", Instrument: "CRFN2022000345679", Status: "Active"},
			{Position: 2, Type: "Mechanic's Lien", Holder: "Brooklyn Renovations LLC", Amount: 28500.00, RecordedDate: "2025-08-19", Instrument: "CRFN2025000789012", Status: "Active"},
		},
		TaxStatus:        TaxInfo{Year: 2025, Status: "Current", Amount: 11420.00, ParcelNumber: "00945-0012-0001"},
		LegalDescription: "ALL THAT CERTAIN PLOT, PIECE OR PARCEL OF LAND, SITUATE, LYING AND BEING IN THE BOROUGH OF BROOKLYN, COUNTY OF KINGS, CITY AND STATE OF NEW YORK, BLOCK 945, LOT 12",
		Easements:        []string{"Con Edison utility easement"},
		LastUpdated:      "2026-03-08T16:45:00Z",
	},

	"oh:cuyahoga:004-14-097": {
		ParcelID: "004-14-097", County: "Cuyahoga", State: "OH",
		Address: "3847 Pearl Rd, Cleveland, OH 44109",
		Owner: OwnerInfo{
			Name: "Sarah J. Williams", VestingType: "Fee Simple",
			DeedDate: "2018-09-28", DeedType: "Warranty Deed", Instrument: "201809280456",
		},
		Liens: []LienInfo{
			{Position: 1, Type: "Mortgage", Holder: "Wells Fargo Bank, N.A.", Amount: 195000.00, RecordedDate: "2018-09-28", Instrument: "201809280457", Status: "Active"},
			{Position: 2, Type: "State Tax Lien", Holder: "Ohio Department of Taxation", Amount: 4310.00, RecordedDate: "2025-06-15", Instrument: "202506150891", Status: "Active"},
		},
		TaxStatus:        TaxInfo{Year: 2025, Status: "Tax Sale Pending", Amount: 6720.00, ParcelNumber: "004-14-097"},
		LegalDescription: "SUBLOT NO. 97, IN PARCEL 14, IN THE CITY OF CLEVELAND, COUNTY OF CUYAHOGA, STATE OF OHIO, AS SHOWN ON THE RECORDED PLAT IN VOLUME 4, PAGE 29",
		Easements:        []string{"FirstEnergy utility easement (Vol. 892, Pg. 34)"},
		LastUpdated:      "2026-03-14T08:20:00Z",
	},

	// NOTE: Case 6 (Patel) — no property data. Mock returns 404 for Dallas/CC-8821-000-0450.
}

// ============================================================
// Mock Data — Court Records
// ============================================================

var courtData = map[string]*CourtRecord{
	// Key: courtCaseNumber (from cases.json)
	"2026-CA-003891": {
		CaseNumber: "2026-CA-003891",
		Court:      "Circuit Court of Miami-Dade County, 11th Judicial Circuit",
		Division:   "Civil",
		Judge:      "Hon. Patricia Navarro",
		FilingDate: "2025-11-20",
		CaseType:   "Foreclosure",
		Status:     "Active",
		Parties:    &Parties{Plaintiff: "Wells Fargo Bank, N.A.", Defendant: "David R. Thompson"},
		Filings: &FilingsList{Items: []Filing{
			{Type: "Complaint", FiledDate: "2025-11-20", DocumentNumber: "2025-CI-28934"},
			{Type: "Lis Pendens", FiledDate: "2025-11-20", DocumentNumber: "2025-CI-28935"},
			{Type: "Summons", FiledDate: "2025-11-21", DocumentNumber: "2025-CI-28980"},
		}},
		NextHearing: &HearingInfo{Date: "2026-04-22", Time: "10:00", Type: "Case Management Conference", Courtroom: "5-3"},
	},

	"2025-CV-18734": {
		CaseNumber: "2025-CV-18734",
		Court:      "District Court of Harris County, 133rd Judicial District",
		Division:   "Civil",
		Judge:      "Hon. James K. Whitfield",
		FilingDate: "2025-06-10",
		CaseType:   "Foreclosure - Non-Judicial",
		Status:     "Active",
		Parties:    &Parties{Plaintiff: "Nationstar Mortgage LLC d/b/a Mr. Cooper", Defendant: "Thanh Nguyen and Linh Nguyen"},
		Filings: &FilingsList{Items: []Filing{
			{Type: "Notice of Default", FiledDate: "2025-06-10", DocumentNumber: "HC-2025-0891234"},
			{Type: "Notice of Substitute Trustee Sale", FiledDate: "2025-12-15", DocumentNumber: "HC-2025-1203456"},
			{Type: "Affidavit of Mailing", FiledDate: "2025-12-16", DocumentNumber: "HC-2025-1203457"},
		}},
		NextHearing: nil,
	},

	"2025-FC-11023": {
		CaseNumber: "2025-FC-11023",
		Court:      "Supreme Court of the State of New York, Kings County",
		Division:   "Foreclosure",
		Judge:      "Hon. Maria E. Ortiz",
		FilingDate: "2025-07-22",
		CaseType:   "Residential Mortgage Foreclosure",
		Status:     "Active",
		Parties:    &Parties{Plaintiff: "JPMorgan Chase Bank, N.A.", Defendant: "Marcus D. Johnson"},
		Filings: &FilingsList{Items: []Filing{
			{Type: "Summons and Complaint", FiledDate: "2025-07-22", DocumentNumber: "KFC-2025-07891"},
			{Type: "Notice of Pendency", FiledDate: "2025-07-22", DocumentNumber: "KFC-2025-07892"},
			{Type: "RPAPL 1303 Notice", FiledDate: "2025-07-23", DocumentNumber: "KFC-2025-07901"},
			{Type: "Affidavit of Service", FiledDate: "2025-08-15", DocumentNumber: "KFC-2025-08234"},
		}},
		NextHearing: &HearingInfo{Date: "2026-05-10", Time: "09:30", Type: "Settlement Conference", Courtroom: "Room 456"},
	},

	"2025-FC-09876": {
		CaseNumber: "2025-FC-09876",
		Court:      "District Court of Dallas County, 160th Judicial District",
		Division:   "Civil",
		Judge:      "Hon. Robert C. Benavides",
		FilingDate: "2025-08-05",
		CaseType:   "Foreclosure",
		Status:     "Active",
		Parties:    &Parties{Plaintiff: "Wells Fargo Bank, N.A.", Defendant: "Vikram Patel"},
		Filings: &FilingsList{Items: []Filing{
			{Type: "Original Petition", FiledDate: "2025-08-05", DocumentNumber: "DC-2025-45678"},
			{Type: "Citation", FiledDate: "2025-08-06", DocumentNumber: "DC-2025-45679"},
		}},
		NextHearing: &HearingInfo{Date: "2026-04-15", Time: "14:00", Type: "Status Conference", Courtroom: "Courtroom 3B"},
	},
}

// ============================================================
// Mock Data — SCRA
// ============================================================

// Key: lowercase last name
var scraData = map[string]*SCRAResult{
	"martinez": {ActiveDuty: false, LastName: "Martinez", FirstName: "Elena", SearchDate: "2026-03-16", CertificateURL: nil},
	"thompson": {ActiveDuty: false, LastName: "Thompson", FirstName: "David", SearchDate: "2026-03-16", CertificateURL: nil},
	"nguyen":   {ActiveDuty: false, LastName: "Nguyen", FirstName: "Thanh", SearchDate: "2026-03-16", CertificateURL: nil},
	"johnson":  {ActiveDuty: true, LastName: "Johnson", FirstName: "Marcus", SearchDate: "2026-03-16", CertificateURL: strPtr("https://scra.dmdc.osd.mil/cert/SCRA-2026-0322-JM")},
	"williams": {ActiveDuty: false, LastName: "Williams", FirstName: "Sarah", SearchDate: "2026-03-16", CertificateURL: nil},
	"patel":    {ActiveDuty: false, LastName: "Patel", FirstName: "Vikram", SearchDate: "2026-03-16", CertificateURL: nil},
}

// In-flight SCRA searches
var scraSearches sync.Map

// ============================================================
// Property Records Service — Port 9001
// ============================================================

func startPropertyService(cfg config) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/properties/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Simulate transient failure
		roll := rand.Float64()
		if roll < cfg.PropertyFailRate {
			log.Printf("[Property Records] %s -> 503 (simulated failure)\n", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "Service temporarily unavailable. Please retry."})
			return
		}

		// Simulate slow response
		if roll < cfg.PropertyFailRate+cfg.PropertySlowRate {
			log.Printf("[Property Records] %s -> 200 (slow: 8s delay)\n", r.URL.Path)
			time.Sleep(8 * time.Second)
		}

		// Parse path: /api/properties/{state}/{county}/{parcelId}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/properties/"), "/")
		if len(parts) < 3 {
			http.Error(w, "Invalid path. Expected: /api/properties/{state}/{county}/{parcelId}", http.StatusBadRequest)
			return
		}
		state := strings.ToLower(parts[0])
		county := strings.ToLower(parts[1])
		parcelID := strings.Join(parts[2:], "/") // parcelId may contain slashes

		key := fmt.Sprintf("%s:%s:%s", state, county, strings.ToLower(parcelID))
		record, ok := propertyData[key]
		if !ok {
			log.Printf("[Property Records] %s -> 404 (not found, key=%s)\n", r.URL.Path, key)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "Property not found", "parcelId": parcelID, "county": county, "state": state})
			return
		}

		log.Printf("[Property Records] %s -> 200\n", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(record)
	})

	server := &http.Server{Addr: ":9001", Handler: mux}
	log.Fatal(server.ListenAndServe())
}

// ============================================================
// Court Records Service — Port 9002
// ============================================================

type courtRateLimiter struct {
	mu       sync.Mutex
	tokens   int
	maxPerSec int
	lastFill time.Time
}

func (rl *courtRateLimiter) allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if now.Sub(rl.lastFill) >= time.Second {
		rl.tokens = rl.maxPerSec
		rl.lastFill = now
	}

	if rl.tokens > 0 {
		rl.tokens--
		return true
	}
	return false
}

func startCourtService(cfg config) {
	limiter := &courtRateLimiter{
		tokens:   cfg.CourtRateLimit,
		maxPerSec: cfg.CourtRateLimit,
		lastFill: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/court-records/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Rate limit check
		if !limiter.allow() {
			log.Printf("[Court Records] POST /api/court-records/search -> 429 (rate limited)\n")
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "Rate limit exceeded. Maximum 2 requests per second.", "retryAfter": "1"})
			return
		}

		// Parse request
		var req struct {
			CaseNumber string `json:"caseNumber"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}

		if req.CaseNumber == "" {
			http.Error(w, "caseNumber is required", http.StatusBadRequest)
			return
		}

		// Look up court data
		record, ok := courtData[req.CaseNumber]
		if !ok {
			// Return a valid XML "not found" response (not an HTTP error)
			notFound := &CourtRecord{
				CaseNumber: req.CaseNumber,
				Status:     "NoFilingFound",
				Message:    "No court filings found for the specified case number.",
			}
			log.Printf("[Court Records] POST /api/court-records/search (case=%s) -> 200 (no filing found)\n", req.CaseNumber)
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(xml.Header))
			xml.NewEncoder(w).Encode(notFound)
			return
		}

		// Marshal to XML
		xmlBytes, err := xml.MarshalIndent(record, "", "  ")
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		fullXML := []byte(xml.Header)
		fullXML = append(fullXML, xmlBytes...)

		// Simulate malformed XML
		if rand.Float64() < cfg.CourtCorruptRate {
			// Truncate response at ~60% to simulate corruption
			cutPoint := len(fullXML) * 6 / 10
			log.Printf("[Court Records] POST /api/court-records/search (case=%s) -> 200 (MALFORMED XML — truncated at byte %d/%d)\n", req.CaseNumber, cutPoint, len(fullXML))
			w.Header().Set("Content-Type", "application/xml")
			w.Write(fullXML[:cutPoint])
			return
		}

		log.Printf("[Court Records] POST /api/court-records/search (case=%s) -> 200\n", req.CaseNumber)
		w.Header().Set("Content-Type", "application/xml")
		w.Write(fullXML)
	})

	server := &http.Server{Addr: ":9002", Handler: mux}
	log.Fatal(server.ListenAndServe())
}

// ============================================================
// SCRA Military Status Service — Port 9003
// ============================================================

func startSCRAService(cfg config) {
	mux := http.NewServeMux()

	// POST /api/scra/search — Submit a search
	mux.HandleFunc("/api/scra/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			LastName  string `json:"lastName"`
			FirstName string `json:"firstName"`
			SSNLast4  string `json:"ssnLast4"`
			DOB       string `json:"dob"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}

		if req.LastName == "" {
			http.Error(w, "lastName is required", http.StatusBadRequest)
			return
		}

		searchID := fmt.Sprintf("scra-%d", time.Now().UnixNano())
		delay := time.Duration(cfg.SCRAMinDelay+rand.Intn(cfg.SCRAMaxDelay-cfg.SCRAMinDelay+1)) * time.Second

		// Determine if this search will fail
		willFail := rand.Float64() < cfg.SCRAFailRate

		search := &scraSearch{
			SearchID:    searchID,
			Status:      "pending",
			SubmittedAt: time.Now(),
			ReadyAt:     time.Now().Add(delay),
		}

		// Look up result data
		key := strings.ToLower(req.LastName)
		if result, ok := scraData[key]; ok && !willFail {
			// Copy the result so we don't share mutable state
			resultCopy := *result
			resultCopy.SearchDate = time.Now().Format("2006-01-02")
			search.Result = &resultCopy
		} else if !willFail {
			// Unknown person — default to not active duty
			search.Result = &SCRAResult{
				ActiveDuty: false,
				LastName:   req.LastName,
				FirstName:  req.FirstName,
				SearchDate: time.Now().Format("2006-01-02"),
			}
		}

		scraSearches.Store(searchID, search)

		// Simulate async processing
		go func() {
			time.Sleep(delay)
			if s, ok := scraSearches.Load(searchID); ok {
				search := s.(*scraSearch)
				search.mu.Lock()
				defer search.mu.Unlock()
				if willFail {
					search.Status = "error"
					search.Error = "Search failed: unable to verify military status. The DMDC database returned an unexpected error."
				} else {
					search.Status = "complete"
				}
			}
		}()

		log.Printf("[SCRA] POST /api/scra/search (name=%s %s) -> 202 (searchId=%s, delay=%s, willFail=%v)\n",
			req.FirstName, req.LastName, searchID, delay, willFail)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"searchId":                   searchID,
			"status":                     "pending",
			"submittedAt":                search.SubmittedAt.Format(time.RFC3339),
			"estimatedCompletionSeconds": int(delay.Seconds()),
		})
	})

	// GET /api/scra/results/{searchId} — Poll for results
	mux.HandleFunc("/api/scra/results/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		searchID := strings.TrimPrefix(r.URL.Path, "/api/scra/results/")
		if searchID == "" {
			http.Error(w, "searchId is required", http.StatusBadRequest)
			return
		}

		s, ok := scraSearches.Load(searchID)
		if !ok {
			log.Printf("[SCRA] GET /api/scra/results/%s -> 404 (not found)\n", searchID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "Search not found", "searchId": searchID})
			return
		}

		search := s.(*scraSearch)
		search.mu.Lock()
		status := search.Status
		search.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch status {
		case "pending":
			log.Printf("[SCRA] GET /api/scra/results/%s -> 202 (still pending)\n", searchID)
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"searchId": searchID,
				"status":   "pending",
			})

		case "complete":
			log.Printf("[SCRA] GET /api/scra/results/%s -> 200 (complete, activeDuty=%v)\n", searchID, search.Result.ActiveDuty)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"searchId":    searchID,
				"status":      "complete",
				"result":      search.Result,
				"completedAt": search.ReadyAt.Format(time.RFC3339),
			})

		case "error":
			log.Printf("[SCRA] GET /api/scra/results/%s -> 200 (error)\n", searchID)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"searchId": searchID,
				"status":   "error",
				"error":    search.Error,
			})
		}
	})

	server := &http.Server{Addr: ":9003", Handler: mux}
	log.Fatal(server.ListenAndServe())
}

// ============================================================
// Helpers
// ============================================================

func strPtr(s string) *string { return &s }

func envFloat(key string, defaultVal float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func envInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

// ============================================================
// Main
// ============================================================

func main() {
	cfg := loadConfig()

	log.Println("========================================")
	log.Println("  Mock External Services")
	log.Println("========================================")
	log.Println()
	log.Printf("  Property Records:  http://localhost:9001  (fail=%.0f%%, slow=%.0f%%)\n", cfg.PropertyFailRate*100, cfg.PropertySlowRate*100)
	log.Printf("  Court Records:     http://localhost:9002  (rate=%d/sec, corrupt=%.0f%%)\n", cfg.CourtRateLimit, cfg.CourtCorruptRate*100)
	log.Printf("  SCRA Check:        http://localhost:9003  (fail=%.0f%%, delay=%d-%ds)\n", cfg.SCRAFailRate*100, cfg.SCRAMinDelay, cfg.SCRAMaxDelay)
	log.Println()
	log.Println("  Override with env vars: PROPERTY_FAIL_RATE, COURT_RATE_LIMIT, etc.")
	log.Println("  Set PROPERTY_FAIL_RATE=0 COURT_CORRUPT_RATE=0 SCRA_FAIL_RATE=0 for clean testing.")
	log.Println("========================================")

	go startPropertyService(cfg)
	go startCourtService(cfg)
	startSCRAService(cfg) // Block on the last one
}
