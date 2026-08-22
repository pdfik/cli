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
)

// JobID is the id every submission gets.
const JobID = "job-1"

// PDF is the download body.
const PDF = "%PDF-1.7 fake"

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

	srv       *httptest.Server
	mu        sync.Mutex
	bodies    []map[string]any
	headers   []http.Header
	polls     int
	submits   int
	downloads int
}

// New starts a fake API and closes it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	f := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /url-to-pdf", f.submit)
	mux.HandleFunc("POST /html-to-pdf", f.submit)
	mux.HandleFunc("GET /jobs/"+JobID, f.poll)
	mux.HandleFunc("GET /jobs/"+JobID+"/download", f.download)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job not found")
	})
	f.srv = httptest.NewServer(mux)
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
		problem(w, f.SubmitStatuses[n], "UPSTREAM", "submit failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"job_id": JobID, "status": "queued", "detail": "Job queued"})
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
	w.Header().Set("Content-Type", "application/pdf")
	w.Write([]byte(PDF))
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
