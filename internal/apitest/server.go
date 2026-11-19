// Package apitest is a fake PDFik API for tests: just enough of the contract
// (see https://api.pdfik.net/openapi.json) to exercise submit → poll → download
// end to end, with knobs for the failure modes the client must survive.
package apitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// JobID is the id every submission gets.
const JobID = "job-1"

// PDF is the download body.
const PDF = "%PDF-1.7 fake"

// PNG is the download body when DownloadContentType is "image/png".
const PNG = "\x89PNG\r\n\x1a\n fake"

// Server is a configurable fake API. Configure the exported fields before the
// first request; read the counters through the accessor methods (handlers
// run in server goroutines).
type Server struct {
	// URL is the base URL to pass to the client.
	URL string

	// PollsUntilDone is how many GET /jobs/{id} calls answer "rendering"
	// before the job is reported done (0 = done on the first poll).
	PollsUntilDone int
	// PollStatuses overrides the HTTP status of successive polls (200 = the
	// normal answer). A non-200 entry is sent with Retry-After: 1 and a
	// problem+json body.
	PollStatuses []int
	// SubmitStatuses overrides the HTTP status of successive submits
	// (202 = the normal answer).
	SubmitStatuses []int
	// FinalStatus replaces the body of the poll that reports the job finished
	// (e.g. {"status":"failed","error_code":"PAGE_TIMEOUT"}).
	FinalStatus map[string]any
	// DownloadHandler replaces the download endpoint when set.
	DownloadHandler http.HandlerFunc
	// DownloadContentType is the download's media type, as the API sends it
	// per job kind ("" = application/pdf). "image/png" also switches the body
	// to PNG — a screenshot job.
	DownloadContentType string

	// SyncHandling picks the answer to a wait:true submission, after any
	// SubmitStatuses override (those model the gateway in front of the hold):
	// "" streams the file (200 + X-PDFik-Job-Id), "timeout" the API's 503
	// #sync-timeout problem, "failed" a 422 #job-failed problem carrying
	// SyncErrorCode, "ignored" the plain async 202 of a server that predates
	// sync mode, "stall" a 200 whose body stops after a few bytes.
	SyncHandling string
	// SyncErrorCode is the job error code of a "failed" answer (default
	// PAGE_TIMEOUT).
	SyncErrorCode string
	// SyncWarning, when set, rides the 200 as X-PDFik-Warning (the e-invoice
	// profile note travels there).
	SyncWarning string
	// SyncContentType is the 200's media type ("" = application/pdf;
	// "image/png" also switches the body to PNG).
	SyncContentType string
	// SyncHeaderDelay postpones the whole 200 (headers included) — for
	// exercising the sync client's response-header timeout.
	SyncHeaderDelay time.Duration
	// AccountStatuses overrides the HTTP status of successive GET /account
	// calls (200 = the normal answer); a non-200 is sent with Retry-After: 1.
	AccountStatuses []int
	// AccountKeyName is the key name GET /account reports (nil = null).
	AccountKeyName *string

	srv       *httptest.Server
	mu        sync.Mutex
	bodies    []map[string]any
	headers   []http.Header
	polls     int
	submits   int
	downloads int
	accounts  int
	// every request's headers, in arrival order
	allHeaders []http.Header
}

// New starts a fake API and closes it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	f := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /url-to-pdf", f.submit)
	mux.HandleFunc("POST /html-to-pdf", f.submit)
	mux.HandleFunc("POST /markdown-to-pdf", f.submit)
	mux.HandleFunc("POST /url-to-image", f.submit)
	mux.HandleFunc("POST /html-to-image", f.submit)
	mux.HandleFunc("POST /einvoice-to-pdf", f.submit)
	mux.HandleFunc("GET /jobs/"+JobID, f.poll)
	mux.HandleFunc("GET /jobs/"+JobID+"/download", f.download)
	mux.HandleFunc("GET /account", f.account)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
	})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.allHeaders = append(f.allHeaders, r.Header.Clone())
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	f.URL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"type":   "https://docs.pdfik.net/error-codes#" + code,
		"title":  http.StatusText(status),
		"status": status,
		"error":  code,
		"detail": detail,
	})
}

func (f *Server) submit(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") == "" {
		problem(w, http.StatusUnauthorized, "MISSING_API_KEY", "Not authenticated")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		problem(w, http.StatusBadRequest, "VALIDATION_ERROR", "bad JSON")
		return
	}
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.headers = append(f.headers, r.Header.Clone())
	n := f.submits
	f.submits++
	f.mu.Unlock()
	if n < len(f.SubmitStatuses) && f.SubmitStatuses[n] != http.StatusAccepted {
		if f.SubmitStatuses[n] == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "2")
			problem(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "Too many requests")
			return
		}
		problem(w, f.SubmitStatuses[n], "UPSTREAM", "submit failed")
		return
	}
	if wait, _ := body["wait"].(bool); wait {
		f.syncReply(w, r)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"job_id": JobID, "status": "queued", "detail": "Job queued"})
}

// syncReply answers a wait:true submission according to SyncHandling.
func (f *Server) syncReply(w http.ResponseWriter, r *http.Request) {
	switch f.SyncHandling {
	case "ignored":
		// A server that predates sync mode: `wait` is an unknown field and the
		// ordinary async 202 comes back.
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"job_id": JobID, "status": "queued", "detail": "Job queued"})
	case "timeout":
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"type":   "https://docs.pdfik.net/error-codes#sync-timeout",
			"title":  "Service Unavailable - Render Did Not Finish In Time",
			"status": http.StatusServiceUnavailable,
			"error":  "SYNC_TIMEOUT",
			"detail": "The render did not finish within 45 seconds. The job was cancelled and NOT charged, and its Idempotency-Key was released, so retrying is safe.",
			"job_id": JobID,
		})
	case "failed":
		code := f.SyncErrorCode
		if code == "" {
			code = "PAGE_TIMEOUT"
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{
			"type":   "https://docs.pdfik.net/error-codes#job-failed",
			"title":  "Unprocessable Entity - Render Failed",
			"status": http.StatusUnprocessableEntity,
			"error":  code,
			"detail": "The render failed with error code " + code + ".",
			"job_id": JobID,
		})
	case "stall":
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("X-PDFik-Job-Id", JobID)
		w.Write([]byte(PDF[:4]))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never send the rest
	default: // the file
		if f.SyncHeaderDelay > 0 {
			time.Sleep(f.SyncHeaderDelay)
		}
		contentType, body := "application/pdf", PDF
		if f.SyncContentType != "" {
			contentType = f.SyncContentType
		}
		if contentType == "image/png" {
			body = PNG
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-PDFik-Job-Id", JobID)
		if f.SyncWarning != "" {
			w.Header().Set("X-PDFik-Warning", f.SyncWarning)
		}
		w.Write([]byte(body))
	}
}

func (f *Server) poll(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	n := f.polls
	f.polls++
	f.mu.Unlock()
	if n < len(f.PollStatuses) && f.PollStatuses[n] != http.StatusOK {
		w.Header().Set("Retry-After", "1")
		problem(w, f.PollStatuses[n], "THROTTLED", "throttled")
		return
	}
	// Only the normal polls count towards "done": overrides are retries.
	normal := n - countNon200(f.PollStatuses, n)
	if normal < f.PollsUntilDone {
		json.NewEncoder(w).Encode(map[string]any{"status": "rendering"})
		return
	}
	if f.FinalStatus != nil {
		json.NewEncoder(w).Encode(f.FinalStatus)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"status": "done", "pages_count": 3,
		"expires_at": "2026-08-23T10:00:00Z",
		"metrics":    map[string]any{"file_size_bytes": len(PDF), "total_duration_ms": 1850, "page_load_ms": 920}})
}

func countNon200(statuses []int, upTo int) int {
	n := 0
	for i := 0; i < upTo && i < len(statuses); i++ {
		if statuses[i] != http.StatusOK {
			n++
		}
	}
	return n
}

func (f *Server) download(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.downloads++
	f.mu.Unlock()
	if f.DownloadHandler != nil {
		f.DownloadHandler(w, r)
		return
	}
	contentType, body := "application/pdf", PDF
	if f.DownloadContentType != "" {
		contentType = f.DownloadContentType
	}
	if contentType == "image/png" {
		body = PNG
	}
	w.Header().Set("Content-Type", contentType)
	w.Write([]byte(body))
}

// LastBody is the most recent submission body.
func (f *Server) LastBody(t testing.TB) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("nothing was submitted")
	}
	return f.bodies[len(f.bodies)-1]
}

// SubmitHeaders returns the headers of every submission, in order.
func (f *Server) SubmitHeaders() []http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]http.Header(nil), f.headers...)
}

// Submits is the number of submission requests received.
func (f *Server) Submits() int { f.mu.Lock(); defer f.mu.Unlock(); return f.submits }

// Polls is the number of status requests received.
func (f *Server) Polls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.polls }

// Downloads is the number of download requests received.
func (f *Server) Downloads() int { f.mu.Lock(); defer f.mu.Unlock(); return f.downloads }

// account answers GET /account with a fixed Pro account.
func (f *Server) account(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") == "" {
		problem(w, http.StatusUnauthorized, "MISSING_API_KEY", "Not authenticated")
		return
	}
	f.mu.Lock()
	n := f.accounts
	f.accounts++
	f.mu.Unlock()
	if n < len(f.AccountStatuses) && f.AccountStatuses[n] != http.StatusOK {
		w.Header().Set("Retry-After", "1")
		problem(w, f.AccountStatuses[n], "RATE_LIMIT_EXCEEDED", "Too many requests")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"plan":   "pro",
		"window": map[string]any{"start": "2026-11-05T00:00:00Z", "end": "2026-12-05T00:00:00Z"},
		"usage":  map[string]any{"documents": 1234, "bytes": 52428800},
		"limits": map[string]any{
			"documents": 50000, "bytes": int64(53687091200), "requests_per_minute": 500,
			"concurrent_jobs": 50, "max_file_size_bytes": 157286400,
		},
		"test_mode": map[string]any{"per_minute": 60, "per_day": 2000},
		"key":       map[string]any{"name": f.AccountKeyName},
	})
}

// Accounts is the number of GET /account requests received.
func (f *Server) Accounts() int { f.mu.Lock(); defer f.mu.Unlock(); return f.accounts }

// AllHeaders returns the headers of every request received, in order.
func (f *Server) AllHeaders() []http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]http.Header(nil), f.allHeaders...)
}
