package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pdfik/cli/internal/apitest"
)

// fakeClock records every sleep and advances a virtual clock, so the polling
// schedule is asserted exactly and the suite never waits on the wall clock.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *fakeClock) total() time.Duration {
	var t time.Duration
	for _, d := range c.sleeps {
		t += d
	}
	return t
}

func newTestClient(t *testing.T, base string, opts ...Option) (*Client, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	opts = append(opts, WithClock(clock.sleep, func() time.Time { return clock.now }))
	c, err := New(base, "sk_live_test", opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c, clock
}

func TestSubmitWaitDownload(t *testing.T) {
	f := apitest.New(t)
	f.PollsUntilDone = 1
	c, _ := newTestClient(t, f.URL)
	ctx := context.Background()

	job, err := c.SubmitURL(ctx, "https://example.com", Submission{"test": true})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if job.JobID != apitest.JobID || job.Status != StatusQueued {
		t.Fatalf("unexpected job: %+v", job)
	}
	if u, _ := f.LastBody(t)["url"].(string); u != "https://example.com" {
		t.Fatalf("submit body lost the url: %v", f.LastBody(t))
	}
	st, err := c.Wait(ctx, job.JobID, 30*time.Second)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if pages, ok := st.Pages(); st.Status != StatusDone || !ok || pages != 3 {
		t.Fatalf("unexpected status: %+v", st)
	}
	var buf bytes.Buffer
	n, err := c.Download(ctx, job.JobID, &buf)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if buf.String() != apitest.PDF || n != int64(len(apitest.PDF)) {
		t.Fatalf("download mismatch: %q (%d bytes)", buf.String(), n)
	}
}

func TestSubmitSendsOneIdempotencyKeyAcrossRetries(t *testing.T) {
	f := apitest.New(t)
	f.SubmitStatuses = []int{503, 202}
	c, clock := newTestClient(t, f.URL)
	job, err := c.SubmitURL(context.Background(), "https://example.com", nil)
	if err != nil {
		t.Fatalf("submit must survive one 503: %v", err)
	}
	if job.JobID != apitest.JobID || f.Submits() != 2 {
		t.Fatalf("expected exactly two submits, got %d", f.Submits())
	}
	hs := f.SubmitHeaders()
	k1, k2 := hs[0].Get("Idempotency-Key"), hs[1].Get("Idempotency-Key")
	if k1 == "" || k1 != k2 {
		t.Fatalf("retries must reuse the idempotency key: %q vs %q", k1, k2)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 1*time.Second {
		t.Fatalf("expected one 1s back-off, got %v", clock.sleeps)
	}
	if ua := hs[0].Get("User-Agent"); !strings.HasPrefix(ua, "pdfik-cli") {
		t.Fatalf("User-Agent not set: %q", ua)
	}
}

func TestSubmitDoesNotRetryClientErrors(t *testing.T) {
	f := apitest.New(t)
	f.SubmitStatuses = []int{422, 202}
	c, clock := newTestClient(t, f.URL)
	_, err := c.SubmitURL(context.Background(), "https://example.com", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 422 {
		t.Fatalf("expected a 422 Error, got %v", err)
	}
	if f.Submits() != 1 || len(clock.sleeps) != 0 {
		t.Fatalf("a 4xx must not be retried: submits=%d sleeps=%v", f.Submits(), clock.sleeps)
	}
}

func TestSubmitGivesUpAfterThreeTransientFailures(t *testing.T) {
	f := apitest.New(t)
	f.SubmitStatuses = []int{502, 503, 504, 202}
	c, clock := newTestClient(t, f.URL)
	_, err := c.SubmitURL(context.Background(), "https://example.com", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 504 {
		t.Fatalf("expected the last 5xx, got %v", err)
	}
	if f.Submits() != 3 || len(clock.sleeps) != 2 {
		t.Fatalf("expected 3 attempts / 2 sleeps, got %d / %v", f.Submits(), clock.sleeps)
	}
}

func TestWaitBackoffScheduleIsFreePlanSafe(t *testing.T) {
	// 2s → 10s, then steady at 10s: never more than 6 polls a minute.
	f := apitest.New(t)
	f.PollsUntilDone = 7
	c, clock := newTestClient(t, f.URL)
	if _, err := c.Wait(context.Background(), apitest.JobID, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 6 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", clock.sleeps, want)
	}
	for i := range want {
		if clock.sleeps[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", clock.sleeps, want)
		}
	}
}

func TestWaitRateLimitHonoursRetryAfterWithCap(t *testing.T) {
	f := apitest.New(t)
	f.PollStatuses = []int{429}
	c, clock := newTestClient(t, f.URL)
	st, err := c.Wait(context.Background(), apitest.JobID, 5*time.Minute)
	if err != nil || st.Status != StatusDone {
		t.Fatalf("wait must survive a 429: %v %+v", err, st)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 1*time.Second { // the fake sends Retry-After: 1
		t.Fatalf("expected one 1s sleep from Retry-After, got %v", clock.sleeps)
	}

	// A huge Retry-After is capped so the server cannot park the client.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
		w.Write([]byte(`{"title":"Too Many Requests","status":429,"detail":"slow down"}`))
	}))
	defer srv.Close()
	c2, clock2 := newTestClient(t, srv.URL)
	_, err = c2.Wait(context.Background(), apitest.JobID, 2*time.Minute)
	var nf *NotFinishedError
	if !errors.As(err, &nf) {
		t.Fatalf("expected NotFinishedError after the timeout, got %v", err)
	}
	if clock2.sleeps[0] != retryAfterCap {
		t.Fatalf("Retry-After must be capped at %s, slept %v", retryAfterCap, clock2.sleeps[0])
	}
	if clock2.total() > 2*time.Minute {
		t.Fatalf("slept past the deadline: %v", clock2.total())
	}
}

func TestWaitReturnsNotFinishedWithHint(t *testing.T) {
	f := apitest.New(t)
	f.PollsUntilDone = 100
	c, clock := newTestClient(t, f.URL)
	st, err := c.Wait(context.Background(), apitest.JobID, 5*time.Second)
	var nf *NotFinishedError
	if !errors.As(err, &nf) || nf.JobID != apitest.JobID || nf.Status != StatusRendering {
		t.Fatalf("expected NotFinishedError, got %v (%+v)", err, st)
	}
	if !strings.Contains(err.Error(), "pdfik status job-1 && pdfik download job-1") {
		t.Fatalf("recovery hint missing: %v", err)
	}
	if clock.total() > 5*time.Second {
		t.Fatalf("slept %v, past the 5s timeout", clock.total())
	}
}

func TestWaitReportsRenderError(t *testing.T) {
	f := apitest.New(t)
	f.FinalStatus = map[string]any{"status": "failed", "error_code": "PAGE_TIMEOUT"}
	c, _ := newTestClient(t, f.URL)
	_, err := c.Wait(context.Background(), apitest.JobID, time.Minute)
	var re *RenderError
	if !errors.As(err, &re) || re.Code != "PAGE_TIMEOUT" || !strings.Contains(err.Error(), "rendering failed (PAGE_TIMEOUT)") {
		t.Fatalf("expected RenderError PAGE_TIMEOUT, got %v", err)
	}
	f2 := apitest.New(t)
	f2.FinalStatus = map[string]any{"status": "failed"}
	c2, _ := newTestClient(t, f2.URL)
	_, err = c2.Wait(context.Background(), apitest.JobID, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "(unknown)") {
		t.Fatalf("missing error_code must read 'unknown', got %v", err)
	}
}

func TestWaitStopsAtOnceOnDeterministicErrors(t *testing.T) {
	for _, status := range []int{401, 402, 403, 404} {
		f := apitest.New(t)
		f.PollStatuses = []int{status, status, status, status}
		c, clock := newTestClient(t, f.URL)
		_, err := c.Wait(context.Background(), apitest.JobID, time.Minute)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Status != status {
			t.Fatalf("%d: expected the API error, got %v", status, err)
		}
		if f.Polls() != 1 || len(clock.sleeps) != 0 || strings.Contains(err.Error(), "may still finish") {
			t.Fatalf("%d: must not be retried: polls=%d sleeps=%v err=%v", status, f.Polls(), clock.sleeps, err)
		}
	}
}

func TestWaitRetriesTransientErrorsThenHints(t *testing.T) {
	f := apitest.New(t)
	f.PollStatuses = []int{502, 502, 502}
	c, clock := newTestClient(t, f.URL)
	_, err := c.Wait(context.Background(), apitest.JobID, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "pdfik status job-1") {
		t.Fatalf("expected the recovery hint, got: %v", err)
	}
	if f.Polls() != 3 || len(clock.sleeps) != 2 {
		t.Fatalf("expected 3 polls / 2 back-offs, got %d / %v", f.Polls(), clock.sleeps)
	}

	// A good poll in between resets the failure counter.
	f2 := apitest.New(t)
	f2.PollsUntilDone = 2
	f2.PollStatuses = []int{502, 502, 200, 502, 502, 200}
	c2, _ := newTestClient(t, f2.URL)
	if _, err := c2.Wait(context.Background(), apitest.JobID, time.Minute); err != nil {
		t.Fatalf("failure counter must reset after a good poll: %v", err)
	}
}

func TestWaitStopsWhenCancelled(t *testing.T) {
	f := apitest.New(t)
	f.PollsUntilDone = 100
	c, _ := newTestClient(t, f.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Wait(ctx, apitest.JobID, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDownloadAbortsWhenTheStreamStalls(t *testing.T) {
	f := apitest.New(t)
	f.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("%PDF"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never send another byte
	}
	c, _ := newTestClient(t, f.URL, WithStallTimeout(200*time.Millisecond))
	start := time.Now()
	var buf bytes.Buffer
	_, err := c.Download(context.Background(), apitest.JobID, &buf)
	var de *DownloadError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "no data for 200ms") {
		t.Fatalf("expected a stall DownloadError, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("stall took %v to detect", time.Since(start))
	}
	if !strings.Contains(err.Error(), "pdfik download job-1") {
		t.Fatalf("recovery hint missing: %v", err)
	}
}

func TestDownloadToleratesSlowButLiveStream(t *testing.T) {
	f := apitest.New(t)
	f.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 10; i++ {
			w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}
	c, _ := newTestClient(t, f.URL, WithStallTimeout(2*time.Second))
	var buf bytes.Buffer
	n, err := c.Download(context.Background(), apitest.JobID, &buf)
	if err != nil || n != 10 {
		t.Fatalf("a live stream must not be cut: n=%d err=%v", n, err)
	}
}

func TestDownloadDoesNotTimeAStalledWriter(t *testing.T) {
	// The watchdog is armed around network reads only: a slow consumer must
	// not trip it (that would burn one of the file's download attempts).
	f := apitest.New(t)
	c, _ := newTestClient(t, f.URL, WithStallTimeout(100*time.Millisecond))
	slow := &slowWriter{delay: 300 * time.Millisecond}
	if _, err := c.Download(context.Background(), apitest.JobID, slow); err != nil {
		t.Fatalf("slow writer must not abort the download: %v", err)
	}
}

type slowWriter struct{ delay time.Duration }

func (w *slowWriter) Write(p []byte) (int, error) { time.Sleep(w.delay); return len(p), nil }

func TestDownloadRetriesBusyButNotExhausted(t *testing.T) {
	// The real API shapes: no `error` code, the case is the fragment of the
	// `type` URL.
	f := apitest.New(t)
	var calls atomic.Int32
	f.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			w.Write([]byte(`{"type":"https://docs.pdfik.net/error-codes#download-busy","title":"Too Many Requests - Download In Progress","status":429,"detail":"busy"}`))
			return
		}
		w.Write([]byte(apitest.PDF))
	}
	c, clock := newTestClient(t, f.URL)
	var buf bytes.Buffer
	if _, err := c.Download(context.Background(), apitest.JobID, &buf); err != nil {
		t.Fatalf("download-busy must be retried: %v", err)
	}
	if calls.Load() != 2 || len(clock.sleeps) != 1 || clock.sleeps[0] != 2*time.Second {
		t.Fatalf("expected one 2s wait then success: calls=%d sleeps=%v", calls.Load(), clock.sleeps)
	}

	f2 := apitest.New(t)
	f2.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
		w.Write([]byte(`{"type":"https://docs.pdfik.net/error-codes#download-attempts-exhausted","title":"Too Many Requests","status":429,"detail":"all attempts used"}`))
	}
	c2, clock2 := newTestClient(t, f2.URL)
	_, err := c2.Download(context.Background(), apitest.JobID, &buf)
	var apiErr *Error
	if !errors.As(err, &apiErr) || !apiErr.IsCase("download-attempts-exhausted") || len(clock2.sleeps) != 0 || f2.Downloads() != 1 {
		t.Fatalf("an exhausted attempt counter must not be retried: err=%v sleeps=%v downloads=%d", err, clock2.sleeps, f2.Downloads())
	}
}

func TestDownloadReportsTruncatedStream(t *testing.T) {
	f := apitest.New(t)
	f.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("%PDF short"))
	}
	c, _ := newTestClient(t, f.URL)
	var buf bytes.Buffer
	_, err := c.Download(context.Background(), apitest.JobID, &buf)
	var de *DownloadError
	if !errors.As(err, &de) || de.Bytes != 10 {
		t.Fatalf("expected a truncation DownloadError after 10 bytes, got %v", err)
	}
}

func TestRedirectPolicyStripsKeyOffOrigin(t *testing.T) {
	var foreignKey, foreignUA, sameHostKey string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignKey = r.Header.Get("X-API-Key")
		foreignUA = r.Header.Get("User-Agent")
		w.Write([]byte(apitest.PDF))
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jobs/j1/download":
			http.Redirect(w, r, foreign.URL+"/file", http.StatusFound)
		case "/jobs/j2/download":
			http.Redirect(w, r, "/jobs/j2/file", http.StatusFound)
		case "/jobs/j2/file":
			sameHostKey = r.Header.Get("X-API-Key")
			w.Write([]byte(apitest.PDF))
		}
	}))
	defer origin.Close()

	c, _ := newTestClient(t, origin.URL)
	var buf bytes.Buffer
	if _, err := c.Download(context.Background(), "j1", &buf); err != nil {
		t.Fatal(err)
	}
	if foreignKey != "" || foreignUA == "" {
		t.Fatalf("X-API-Key leaked to a foreign host (%q) or request never arrived", foreignKey)
	}
	buf.Reset()
	if _, err := c.Download(context.Background(), "j2", &buf); err != nil {
		t.Fatal(err)
	}
	if sameHostKey != "sk_live_test" {
		t.Fatalf("same-host redirect must keep the key, got %q", sameHostKey)
	}
}

func TestErrorBodiesAreSummarisedNotDumped(t *testing.T) {
	page := "<html><body>" + strings.Repeat("x", 20_000) + "</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/url-to-pdf" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(502)
			w.Write([]byte(page))
			return
		}
		w.WriteHeader(500)
		w.Write([]byte(strings.Repeat("plain text ", 100)))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.SubmitURL(context.Background(), "https://example.com", nil)
	if err == nil || strings.Contains(err.Error(), "xxxx") || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Fatalf("HTML error page must be summarised, got %d chars: %.120s", len(err.Error()), err)
	}
	_, err = c.Status(context.Background(), "j1")
	if err == nil || len(err.Error()) > 400 {
		t.Fatalf("long text bodies must be capped, got %d chars", len(err.Error()))
	}
}

func TestValidationErrorsAreFlattened(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"type":"https://docs.pdfik.net/error-codes#validation-error","title":"Unprocessable Entity","status":422,
			"detail":[{"loc":["body","options","format"],"msg":"Value error, options.format must be one of A4, Letter","type":"value_error"}]}`))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.SubmitURL(context.Background(), "https://example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "options.format: options.format must be one of A4, Letter") {
		t.Fatalf("422 detail list not flattened: %v", err)
	}
}

func TestStructuredErrorKeepsCodeAndRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
		w.Write([]byte(`{"type":"https://docs.pdfik.net/error-codes#concurrency-limit","title":"Too Many Requests","status":429,
			"error":"CONCURRENCY_LIMIT_EXCEEDED","detail":"You already have 50 jobs queued"}`))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.SubmitURL(context.Background(), "https://example.com", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "CONCURRENCY_LIMIT_EXCEEDED" || apiErr.RetryAfterHint() != "5" {
		t.Fatalf("unexpected error: %+v", err)
	}
	if !strings.Contains(apiErr.Error(), "50 jobs queued") || !strings.Contains(apiErr.Error(), "reference: https://docs.pdfik.net") {
		t.Fatalf("detail or reference lost: %s", apiErr.Error())
	}
}

func TestWireTextIsSanitized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jobs/j1" {
			w.Write([]byte(`{"status":"done\u001b[31m","error_code":"\u009bX"}`))
			return
		}
		w.WriteHeader(400)
		w.Write([]byte(`{"title":"Bad","status":400,"detail":"bad\u001b[2Jthing"}`))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	st, err := c.Status(context.Background(), "j1")
	if err != nil || st.Status != "done[31m" || st.ErrorCode != "X" {
		t.Fatalf("control characters survived: %+v %v", st, err)
	}
	_, err = c.SubmitURL(context.Background(), "https://example.com", nil)
	if err == nil || strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), "bad[2Jthing") {
		t.Fatalf("error detail not sanitized: %q", err)
	}
}

func TestMalformedJobIDIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		w.Write([]byte(`{"job_id":"../../etc","status":"queued"}`))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.SubmitURL(context.Background(), "https://example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "malformed job id") {
		t.Fatalf("expected a malformed-id error, got %v", err)
	}
	if ValidJobID("") || ValidJobID("a/b") || !ValidJobID("22d01579-870f-4268-ab75-de8f887cd9cd") {
		t.Fatal("ValidJobID shape check wrong")
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	if _, err := New("https://api.pdfik.net", ""); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("missing key must be refused, got %v", err)
	}
	if _, err := New("http://internal.example.com", "k"); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Fatalf("plain http to a remote host must be refused, got %v", err)
	}
	if _, err := New("http://127.0.0.1:1", "k"); err != nil {
		t.Fatalf("loopback http must be allowed: %v", err)
	}
	if _, err := New("http://internal.example.com", "k", WithInsecureHTTP()); err != nil {
		t.Fatalf("WithInsecureHTTP must allow it: %v", err)
	}
	if _, err := New("ftp://x", "k"); err == nil {
		t.Fatal("unsupported scheme must be refused")
	}
	if host, plain := PlainHTTPHost("http://lab.internal:8080/"); !plain || host != "lab.internal:8080" {
		t.Fatalf("PlainHTTPHost = %q,%v", host, plain)
	}
	if _, plain := PlainHTTPHost("http://localhost:8000"); plain {
		t.Fatal("loopback is not plain-http in the warning sense")
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 45_678: "44.6 KB", 3 << 20: "3.0 MB"} {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestStripKeyOnRedirectDowngradeAndCap(t *testing.T) {
	first, _ := http.NewRequest(http.MethodGet, "https://api.example/jobs/1/download", nil)
	next, _ := http.NewRequest(http.MethodGet, "http://api.example/file", nil)
	next.Header.Set("X-API-Key", "k")
	if err := stripKeyOnRedirect(next, []*http.Request{first}); err != nil || next.Header.Get("X-API-Key") != "" {
		t.Fatalf("same-host https->http downgrade must drop the key: err=%v key=%q", err, next.Header.Get("X-API-Key"))
	}
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = first
	}
	if err := stripKeyOnRedirect(next, via); err == nil {
		t.Fatal("a redirect loop must be stopped")
	}
}

func TestPerRequestTimeoutIsTransientNotCancellation(t *testing.T) {
	// Since Go 1.23 a Client.Timeout error also satisfies errors.Is(err,
	// context.DeadlineExceeded); it must still be retried like any blip.
	var slow atomic.Bool
	slow.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.CompareAndSwap(true, false) {
			time.Sleep(300 * time.Millisecond)
		}
		w.Write([]byte(`{"status":"done","pages_count":1}`))
	}))
	defer srv.Close()
	c, clock := newTestClient(t, srv.URL, WithRequestTimeout(50*time.Millisecond))
	st, err := c.Wait(context.Background(), apitest.JobID, time.Minute)
	if err != nil || st.Status != StatusDone {
		t.Fatalf("a single slow poll must be retried: %v %+v", err, st)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != pollFailureBackoff {
		t.Fatalf("expected one transient back-off, got %v", clock.sleeps)
	}
}

func TestSubmitIsNotRetriedAfterCancellation(t *testing.T) {
	f := apitest.New(t)
	f.SubmitStatuses = []int{503, 202}
	c, _ := newTestClient(t, f.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.SubmitURL(ctx, "https://example.com", nil)
	if !errors.Is(err, context.Canceled) || f.Submits() != 0 {
		t.Fatalf("a cancelled context must not submit at all: err=%v submits=%d", err, f.Submits())
	}
}

func TestDownloadRetriesServiceUnavailable(t *testing.T) {
	f := apitest.New(t)
	var calls atomic.Int32
	f.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(503)
			w.Write([]byte(`{"type":"https://docs.pdfik.net/error-codes#service-unavailable","title":"Service Unavailable","status":503,"detail":"limits unavailable"}`))
			return
		}
		w.Write([]byte(apitest.PDF))
	}
	c, clock := newTestClient(t, f.URL)
	var buf bytes.Buffer
	if _, err := c.Download(context.Background(), apitest.JobID, &buf); err != nil {
		t.Fatalf("503 must be retried up to the cap: %v", err)
	}
	if calls.Load() != 3 || len(clock.sleeps) != 2 {
		t.Fatalf("expected 3 attempts / 2 sleeps, got %d / %v", calls.Load(), clock.sleeps)
	}
}

func TestObjectDetailsAreReadable(t *testing.T) {
	cases := []struct{ body, want string }{
		{
			`{"type":"https://docs.pdfik.net/error-codes#402","title":"HTTP Error","status":402,"detail":{"error":"PAYMENT_METHOD_REQUIRED","message":"No active payment method on file.","add_card_url":"https://pdfik.net/dashboard/billing"}}`,
			"API error 402 (PAYMENT_METHOD_REQUIRED): No active payment method on file. — https://pdfik.net/dashboard/billing",
		},
		{
			`{"type":"https://docs.pdfik.net/error-codes#402","title":"HTTP Error","status":402,"detail":{"error":"PLAN_UPGRADE_REQUIRED","feature":"auth","requiredPlan":"pro","upgrade_url":"https://pdfik.net/dashboard/billing"}}`,
			"API error 402 (PLAN_UPGRADE_REQUIRED): auth requires the pro plan or higher — https://pdfik.net/dashboard/billing",
		},
	}
	for _, tc := range cases {
		body := tc.body
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(402)
			w.Write([]byte(body))
		}))
		c, _ := newTestClient(t, srv.URL)
		_, err := c.SubmitURL(context.Background(), "https://example.com", nil)
		srv.Close()
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Fatalf("got %q\nwant prefix %q", err, tc.want)
		}
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"5", 5 * time.Second},
		{"10000000000", retryAfterCap}, // would overflow time.Duration
		{"0", retryAfterFallback},
		{"", retryAfterFallback},
		{"garbage", retryAfterFallback},
		{"Sat, 22 Aug 2026 12:00:30 GMT", 30 * time.Second},
		{"Sat, 22 Aug 2026 11:00:00 GMT", retryAfterFallback}, // in the past
		{"Sat, 22 Aug 2026 14:00:00 GMT", retryAfterCap},
	}
	for _, tc := range cases {
		e := &Error{RetryAfter: tc.in}
		if got := e.retryAfter(retryAfterFallback, now); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLongErrorTextIsCutOnRuneBoundary(t *testing.T) {
	text := strings.Repeat("ü", 400)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(text))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv.URL)
	_, err := c.Status(context.Background(), "j1")
	if err == nil || !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "…") {
		t.Fatalf("expected a valid, capped message, got %q", err)
	}
}
