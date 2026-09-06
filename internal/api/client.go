// Package api is a minimal client for the PDFik REST API.
//
// It speaks to the five published endpoints the CLI needs — submit a URL, an
// HTML document or an e-invoice XML, poll a job, download the result — and
// nothing else. The full contract lives at https://api.pdfik.net/openapi.json.
// Standard library only: zero dependencies is part of the tool's promise and
// keeps the supply chain empty.
//
// Every network call takes a context.Context, so the caller can cancel a poll
// or a download (the CLI wires Ctrl-C to it). Time is injectable for tests.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://api.pdfik.net"

// Job statuses as reported by the API.
const (
	StatusQueued    = "queued"
	StatusRendering = "rendering"
	StatusUploading = "uploading"
	StatusDone      = "done"
	StatusFailed    = "failed"
)

// Tuning. Pacing is a contract, not a style choice: the Free plan allows 10
// requests per minute on a sliding window and the submit already spent one, so
// polling backs off from 2 s to 10 s and stays there (6/min steady state).
const (
	requestTimeout        = 60 * time.Second
	responseHeaderTimeout = 30 * time.Second
	defaultStallTimeout   = 90 * time.Second
	maxRedirects          = 10

	pollInitial        = 2 * time.Second
	pollStep           = 2 * time.Second
	pollMax            = 10 * time.Second
	pollFailureLimit   = 3
	pollFailureBackoff = 3 * time.Second
	retryAfterFallback = 10 * time.Second
	retryAfterCap      = 60 * time.Second // the server controls the header; the caller's timeout stays in charge

	submitAttempts   = 3
	downloadAttempts = 3

	errorBodyLimit   = 64 << 10
	errorDetailLimit = 300
	responseLimit    = 1 << 20
)

var submitBackoff = [submitAttempts - 1]time.Duration{1 * time.Second, 3 * time.Second}

// jobIDPattern is the shape of an API job id (UUIDv4 today). Ids come back over
// the wire and end up in URL paths, file names and terminal output, so they are
// checked once, at the boundary.
var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// ValidJobID reports whether id has the shape of a job id.
func ValidJobID(id string) bool { return jobIDPattern.MatchString(id) }

// Client talks to one API base URL with one API key.
type Client struct {
	baseURL        string
	apiKey         string
	userAgent      string
	insecureHTTP   bool
	stallTimeout   time.Duration
	requestTimeout time.Duration

	// Submissions and polls answer in milliseconds — a hard timeout fits them.
	// The download streams a file that can be 150 MB and each started stream
	// consumes one of the file's three download attempts, so it gets a client
	// WITHOUT an overall deadline: aborting a slow-link download halfway would
	// waste an attempt on every retry. It still has a response-header timeout
	// and a stall watchdog, so a dead connection cannot hang forever.
	http     *http.Client
	download *http.Client

	// Injectable time, so the polling schedule is testable without sleeping.
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithUserAgent sets the User-Agent header (the CLI reports its version).
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithInsecureHTTP allows a plain-http base URL on a non-loopback host. The
// caller is expected to warn the user; the client itself never prints.
func WithInsecureHTTP() Option { return func(c *Client) { c.insecureHTTP = true } }

// WithStallTimeout sets how long a download may deliver no bytes before it is
// aborted (default 90 s).
func WithStallTimeout(d time.Duration) Option { return func(c *Client) { c.stallTimeout = d } }

// WithClock replaces the sleep and clock functions (tests).
func WithClock(sleep func(context.Context, time.Duration) error, now func() time.Time) Option {
	return func(c *Client) { c.sleep, c.now = sleep, now }
}

// WithRequestTimeout sets the per-request deadline for submissions and polls
// (default 60 s). Downloads are bounded by the stall watchdog instead.
func WithRequestTimeout(d time.Duration) Option { return func(c *Client) { c.requestTimeout = d } }

// ErrNoAPIKey is returned by New when no key was given.
var ErrNoAPIKey = errors.New("no API key: set PDFIK_API_KEY or pass --api-key (create one at https://pdfik.net/dashboard/api-keys)")

// New validates the configuration once and returns a ready client.
func New(baseURL, apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, ErrNoAPIKey
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	c := &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		userAgent:      "pdfik-cli",
		stallTimeout:   defaultStallTimeout,
		requestTimeout: requestTimeout,
		sleep:          sleepContext,
		now:            time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	if err := checkBaseURL(c.baseURL, c.insecureHTTP); err != nil {
		return nil, err
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	downloadTransport := transport.Clone()
	downloadTransport.ResponseHeaderTimeout = responseHeaderTimeout
	c.http = &http.Client{Timeout: c.requestTimeout, Transport: transport.Clone(), CheckRedirect: stripKeyOnRedirect}
	c.download = &http.Client{Transport: downloadTransport, CheckRedirect: stripKeyOnRedirect}
	return c, nil
}

// PlainHTTPHost reports the host when baseURL uses plain http to something
// other than loopback — the one case that needs an explicit opt-in.
func PlainHTTPHost(baseURL string) (host string, plain bool) {
	if baseURL == "" {
		return "", false
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" {
		return "", false
	}
	if isLoopback(u.Hostname()) {
		return "", false
	}
	return u.Host, true
}

// isLoopback is an address test, not a string test: "127.evil.example" is a
// registrable name and must not pass as loopback.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkBaseURL refuses to send the API key over cleartext http to anything but
// loopback (loopback keeps local development and the test-suite working).
func checkBaseURL(base string, insecureAllowed bool) error {
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("--api-url: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) || insecureAllowed {
			return nil
		}
		return fmt.Errorf("refusing to send the API key over plain http to %s — use https, or set PDFIK_INSECURE_HTTP=1 if you really mean it", u.Host)
	default:
		return fmt.Errorf("--api-url: unsupported scheme %q (use https://host)", u.Scheme)
	}
}

// stripKeyOnRedirect lets redirects work but never lets the API key leave the
// origin it was meant for: Go copies custom headers such as X-API-Key to every
// redirect target (only Authorization and Cookie are stripped automatically),
// so a 3xx to presigned storage — or an https→http downgrade on the same host —
// would otherwise leak the key into third-party logs.
func stripKeyOnRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	first := via[0].URL
	if req.URL.Host != first.Host || (first.Scheme == "https" && req.URL.Scheme != "https") {
		req.Header.Del("X-API-Key")
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Job is the API's answer to a submission.
type Job struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// JobStatus is one poll of GET /jobs/{id}.
type JobStatus struct {
	Status     string   `json:"status"`
	Test       bool     `json:"test"`
	PagesCount *int     `json:"pages_count"`
	ErrorCode  string   `json:"error_code"`
	ExpiresAt  string   `json:"expires_at"`
	Metrics    *Metrics `json:"metrics"`
}

// Metrics is what the API reports about a finished render (all optional).
type Metrics struct {
	FileSizeBytes   *int64 `json:"file_size_bytes"`
	PageLoadMs      *int   `json:"page_load_ms"`
	ProcessingMs    *int   `json:"processing_ms"`
	TotalDurationMs *int   `json:"total_duration_ms"`
	PageCount       *int   `json:"page_count"`
}

// Pages returns the page count from whichever field the API filled in.
func (s JobStatus) Pages() (int, bool) {
	if s.PagesCount != nil {
		return *s.PagesCount, true
	}
	if s.Metrics != nil && s.Metrics.PageCount != nil {
		return *s.Metrics.PageCount, true
	}
	return 0, false
}

func (s *JobStatus) sanitize() {
	s.Status = SanitizeText(s.Status)
	s.ErrorCode = SanitizeText(s.ErrorCode)
	s.ExpiresAt = SanitizeText(s.ExpiresAt)
}

// Submission is a request body. Bodies are loose maps so that only the options
// the user actually set are sent — the API's defaults stay the API's business.
type Submission map[string]any

// SubmitURL asks the API to render a public URL.
func (c *Client) SubmitURL(ctx context.Context, target string, opts Submission) (Job, error) {
	body := Submission{"url": target}
	for k, v := range opts {
		body[k] = v
	}
	return c.submit(ctx, "/url-to-pdf", body)
}

// SubmitHTML asks the API to render an HTML document.
func (c *Client) SubmitHTML(ctx context.Context, html string, opts Submission) (Job, error) {
	body := Submission{"html": html}
	for k, v := range opts {
		body[k] = v
	}
	return c.submit(ctx, "/html-to-pdf", body)
}

// SubmitEInvoice asks the API to build a Factur-X (PDF/A-3) e-invoice from
// UN/CEFACT CII XML.
func (c *Client) SubmitEInvoice(ctx context.Context, xml string, opts Submission) (Job, error) {
	body := Submission{"xml": xml}
	for k, v := range opts {
		body[k] = v
	}
	return c.submit(ctx, "/einvoice-to-pdf", body)
}

// submit posts the body once per attempt under ONE Idempotency-Key, so a
// retried request after a lost response can never create — and charge — a
// second job. Only transport failures and 502/503/504 are retried.
func (c *Client) submit(ctx context.Context, path string, body Submission) (Job, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Job{}, err
	}
	key, err := newIdempotencyKey()
	if err != nil {
		return Job{}, err
	}
	for attempt := 1; ; attempt++ {
		req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(payload))
		if err != nil {
			return Job{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		var job Job
		err = c.do(req, http.StatusAccepted, &job)
		if err == nil {
			if !ValidJobID(job.JobID) {
				return Job{}, fmt.Errorf("the API returned a malformed job id %q", SanitizeText(job.JobID))
			}
			job.Status = SanitizeText(job.Status)
			return job, nil
		}
		if attempt >= submitAttempts || !retryableSubmit(ctx, err) {
			return Job{}, err
		}
		if err := c.sleep(ctx, submitBackoff[attempt-1]); err != nil {
			return Job{}, err
		}
	}
}

func newIdempotencyKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating an idempotency key: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// retryableSubmit decides on the caller's context, not on the error chain:
// since Go 1.23 a per-request timeout also satisfies errors.Is(err,
// context.DeadlineExceeded), and a lost response IS the case the idempotency
// key makes safe to retry. Certificate problems are deterministic and are not.
func retryableSubmit(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusBadGateway ||
			apiErr.Status == http.StatusServiceUnavailable ||
			apiErr.Status == http.StatusGatewayTimeout
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return false
	}
	return !isCertificateError(urlErr.Err)
}

func isCertificateError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	return errors.As(err, &unknownAuthority) || errors.As(err, &hostname) ||
		errors.As(err, &invalid) || errors.As(err, &record)
}

// Status fetches the job state once.
func (c *Client) Status(ctx context.Context, jobID string) (JobStatus, error) {
	var st JobStatus
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+url.PathEscape(jobID), nil)
	if err != nil {
		return st, err
	}
	if err := c.do(req, http.StatusOK, &st); err != nil {
		return st, err
	}
	st.sanitize()
	return st, nil
}

// Wait polls the job until it is done, failed, cancelled or out of time.
//
// A 429 mid-poll is a throttle, not a failure: the poll sleeps out
// Retry-After (capped) and keeps going. Transient failures (network blips,
// 5xx) are retried a few times before giving up with a recovery hint; any
// other 4xx is returned at once — a revoked key or a foreign job id does not
// get better with waiting.
func (c *Client) Wait(ctx context.Context, jobID string, timeout time.Duration) (JobStatus, error) {
	var last JobStatus
	deadline := c.now().Add(timeout)
	interval := pollInitial
	failures := 0
	for {
		st, err := c.Status(ctx, jobID)
		switch {
		case err == nil:
			last, failures = st, 0
			switch st.Status {
			case StatusDone:
				return st, nil
			case StatusFailed:
				return st, &RenderError{JobID: jobID, Code: st.ErrorCode}
			}
		case ctx.Err() != nil:
			// The caller's cancellation — not a per-request timeout, which
			// since Go 1.23 also matches context.DeadlineExceeded and belongs
			// to the transient branch below.
			return last, ctx.Err()
		case isRateLimited(err):
			var apiErr *Error
			errors.As(err, &apiErr)
			if err := c.sleepUntil(ctx, deadline, apiErr.retryAfter(retryAfterFallback, c.now)); err != nil {
				return last, c.notFinished(jobID, last, timeout, err)
			}
			continue
		case isTransient(err):
			failures++
			if failures >= pollFailureLimit {
				return last, fmt.Errorf("%w\n  the job itself may still finish — check later with: pdfik status %s && pdfik download %s", err, jobID, jobID)
			}
			if err := c.sleepUntil(ctx, deadline, pollFailureBackoff); err != nil {
				return last, c.notFinished(jobID, last, timeout, err)
			}
			continue
		default:
			return last, err
		}
		if err := c.sleepUntil(ctx, deadline, interval); err != nil {
			return last, c.notFinished(jobID, last, timeout, err)
		}
		if interval < pollMax {
			interval += pollStep
		}
	}
}

var errDeadline = errors.New("deadline reached")

// sleepUntil sleeps for d, but never past deadline; errDeadline when there is
// no time left at all. The caller still gets one last poll after a sleep that
// ended exactly on the deadline.
func (c *Client) sleepUntil(ctx context.Context, deadline time.Time, d time.Duration) error {
	remaining := deadline.Sub(c.now())
	if remaining <= 0 {
		return errDeadline
	}
	if d <= 0 {
		d = retryAfterFallback // never re-poll without sleeping
	}
	if d > remaining {
		d = remaining
	}
	return c.sleep(ctx, d)
}

func (c *Client) notFinished(jobID string, last JobStatus, timeout time.Duration, cause error) error {
	if !errors.Is(cause, errDeadline) {
		return cause // context cancelled
	}
	return &NotFinishedError{JobID: jobID, Status: last.Status, After: timeout}
}

func isRateLimited(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests
}

// isTransient: network and decoding problems, and server-side 5xx. Deterministic
// 4xx (401, 402, 403, 404, 422) are not.
func isTransient(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 500
	}
	return true
}

// Download streams the finished PDF into w and returns the byte count.
//
// Attempts are counted by the API when a stream STARTS (three per file), so
// the caller writes straight to the destination — there is no second
// "verification" download. The two rejections the API documents as free
// (429 download-busy, 503) are waited out and retried.
func (c *Client) Download(ctx context.Context, jobID string, w io.Writer) (int64, error) {
	for attempt := 1; ; attempt++ {
		n, apiErr, err := c.downloadOnce(ctx, jobID, w)
		if err != nil || apiErr == nil {
			return n, err
		}
		// Only the rejections the API documents as free are worth waiting out:
		// another stream of this file is active (DOWNLOAD_BUSY), or the
		// limit service is briefly unavailable (503). An exhausted attempt
		// counter is also a 429 — with a Retry-After of an hour — and must not
		// be retried.
		retryable := apiErr.Status == http.StatusServiceUnavailable || apiErr.IsCase("download-busy")
		if attempt >= downloadAttempts || !retryable {
			return 0, apiErr
		}
		if err := c.sleep(ctx, apiErr.retryAfter(retryAfterFallback, c.now)); err != nil {
			return 0, err
		}
	}
}

// downloadOnce performs one download request. A rejection comes back as
// apiErr (so the caller can decide to retry); anything else as err.
func (c *Client) downloadOnce(parent context.Context, jobID string, w io.Writer) (n int64, apiErr *Error, err error) {
	// The request is bound to a context the stall watchdog can cancel; the
	// caller's context is still honoured through it.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+url.PathEscape(jobID)+"/download", nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.download.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, errorFrom(resp), nil
	}
	// Attempts are counted when the stream starts, so the bytes go straight to
	// w. The watchdog is armed only around network reads (see stallReader), so
	// a slow consumer of w — a paused pipe, a slow disk — never trips it.
	timer := time.AfterFunc(c.stallTimeout, cancel)
	timer.Stop()
	n, err = io.Copy(w, &stallReader{r: resp.Body, timer: timer, stallAfter: c.stallTimeout})
	if err != nil {
		if parent.Err() != nil {
			return n, nil, parent.Err()
		}
		reason := err.Error()
		if ctx.Err() != nil {
			reason = fmt.Sprintf("no data for %s", c.stallTimeout)
		}
		return n, nil, &DownloadError{JobID: jobID, Bytes: n, Reason: reason}
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return n, nil, &DownloadError{JobID: jobID, Bytes: n, Reason: fmt.Sprintf("got %d of %d bytes", n, resp.ContentLength)}
	}
	return n, nil, nil
}

// stallReader aborts the response when no bytes arrive for stallAfter. A slow
// link that is still delivering resets the timer on every read, so genuine
// slow downloads are never cut — only dead connections are.
type stallReader struct {
	r          io.Reader
	timer      *time.Timer
	stallAfter time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	s.timer.Reset(s.stallAfter)
	n, err := s.r.Read(p)
	s.timer.Stop()
	return n, err
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json, application/pdf")
	return req, nil
}

// do performs a request that must answer wantStatus with a JSON body.
func (c *Client) do(req *http.Request, wantStatus int, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return errorFrom(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, responseLimit)).Decode(out); err != nil {
		return fmt.Errorf("unexpected response from %s %s: %w", req.Method, req.URL.Path, err)
	}
	return nil
}

// HumanBytes formats a byte count the way people read it.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// SanitizeText strips terminal control characters (C0 except \n and \t, DEL,
// C1) from text that arrived over the network. A hostile or broken endpoint
// must not be able to inject escape sequences into the user's terminal.
func SanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
