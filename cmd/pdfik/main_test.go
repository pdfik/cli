package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pdfik/cli/internal/api"
	"github.com/pdfik/cli/internal/apitest"
)

func TestMain(m *testing.M) {
	// Hermetic: any accidental network call fails instantly and offline, and no
	// developer key can leak into a test.
	os.Setenv("PDFIK_API_URL", "http://127.0.0.1:1")
	os.Unsetenv("PDFIK_API_KEY")
	os.Unsetenv("PDFIK_INSECURE_HTTP")
	// Polling must not sleep on the wall clock in command-level tests.
	extraClientOptions = []api.Option{api.WithClock(
		func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		time.Now,
	)}
	os.Exit(m.Run())
}

type result struct {
	code   int
	stdout string
	stderr string
}

// execute runs the CLI against the fake API (when f is not nil) with the test
// key, returning exit code and both streams.
func execute(t *testing.T, f *apitest.Server, stdin io.Reader, args ...string) result {
	t.Helper()
	return executeCtx(t, context.Background(), f, stdin, args...)
}

func executeCtx(t *testing.T, ctx context.Context, f *apitest.Server, stdin io.Reader, args ...string) result {
	t.Helper()
	if f != nil {
		args = append(args, "--api-key", "sk_live_test", "--api-url", f.URL)
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	var stdout, stderr bytes.Buffer
	code := run(ctx, args, streams{in: stdin, out: &stdout, err: &stderr})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func mustContain(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("expected %q in:\n%s", want, s)
	}
}

func TestFlagsBeforeAndAfterThePositionalArgument(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--test", "-o", dir, "-f", "x.pdf")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.pdf")); err != nil {
		t.Fatalf("output not written: %v", err)
	}
	if f.LastBody(t)["test"] != true {
		t.Fatalf("--test not sent: %v", f.LastBody(t))
	}
	r = execute(t, f, nil, "url-to-pdf", "--test", "-o", dir, "-f", "before.pdf", "https://example.com")
	if r.code != exitOK {
		t.Fatalf("flags before the positional: exit %d: %s", r.code, r.stderr)
	}
}

func TestPageOptionsReachTheAPI(t *testing.T) {
	f := apitest.New(t)
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--no-background", "--format", "A3", "--landscape",
		"--margin", "10mm", "--margin-top", "25mm", "-o", t.TempDir())
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	opts, _ := f.LastBody(t)["options"].(map[string]any)
	if opts["print_background"] != false || opts["format"] != "A3" || opts["landscape"] != true {
		t.Fatalf("unexpected options: %v", opts)
	}
	m, _ := opts["margin"].(map[string]any)
	if m["top"] != "25mm" || m["right"] != "10mm" || m["bottom"] != "10mm" || m["left"] != "10mm" {
		t.Fatalf("unexpected margin: %v", m)
	}
}

func TestClientSideValidationIsAUsageError(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown format", []string{"url-to-pdf", "https://example.com", "--format", "B5"}, "Ledger"},
		{"margin without unit", []string{"url-to-pdf", "https://example.com", "--margin", "10"}, "not a valid length"},
		{"side margin without unit", []string{"url-to-pdf", "https://example.com", "--margin-bottom", "5"}, "--margin-bottom"},
		{"timeout without unit", []string{"url-to-pdf", "https://example.com", "--timeout", "90"}, "examples: 90s, 3m"},
		{"zero timeout", []string{"url-to-pdf", "https://example.com", "--timeout", "0s"}, "not a valid duration"},
		{"-o pointing at a file name", []string{"url-to-pdf", "https://example.com", "-o", "report.pdf"}, "--file-name"},
		{"-f with a path", []string{"url-to-pdf", "https://example.com", "-f", "sub/dir.pdf"}, "bare file name"},
		{"two positionals", []string{"url-to-pdf", "--format", "A4", "--", "https://example.com", "--test"}, "exactly one input"},
		{"unknown flag", []string{"url-to-pdf", "https://example.com", "--bogus"}, "bogus"},
		{"missing key", []string{"url-to-pdf", "https://example.com", "--api-url", "http://127.0.0.1:1"}, "PDFIK_API_KEY"},
		{"plain http base URL", []string{"url-to-pdf", "https://example.com", "--api-key", "k", "--api-url", "http://internal.example.com"}, "plain http"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := execute(t, nil, nil, tc.args...)
			if r.code != exitUsage {
				t.Fatalf("exit %d, want %d: %s", r.code, exitUsage, r.stderr)
			}
			mustContain(t, r.stderr, tc.want)
			mustContain(t, r.stderr, "Run 'pdfik help' for usage.")
			if strings.Contains(r.stderr, "Usage:") {
				t.Fatalf("a flag error must not dump the whole usage text:\n%s", r.stderr)
			}
		})
	}
}

func TestHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"url-to-pdf", "--help"}, {"download", "-h"}} {
		r := execute(t, nil, nil, args...)
		if r.code != exitOK || !strings.Contains(r.stdout, "Usage:") || !strings.Contains(r.stdout, "Exit codes:") {
			t.Fatalf("%v: exit %d stdout=%q", args, r.code, r.stdout)
		}
	}
	r := execute(t, nil, nil, "version")
	if r.code != exitOK || !strings.HasPrefix(r.stdout, "pdfik ") {
		t.Fatalf("version: %d %q", r.code, r.stdout)
	}
	if r := execute(t, nil, nil); r.code != exitUsage {
		t.Fatalf("no arguments must be a usage error, got %d", r.code)
	}
	if r := execute(t, nil, nil, "nosuch"); r.code != exitUsage || !strings.Contains(r.stderr, "unknown command") {
		t.Fatalf("unknown command: %d %q", r.code, r.stderr)
	}
}

func TestStdoutStreamingKeepsStdoutClean(t *testing.T) {
	f := apitest.New(t)
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", "-")
	if r.code != exitOK || r.stdout != apitest.PDF {
		t.Fatalf("stdout must carry exactly the PDF: code=%d stdout=%q stderr=%q", r.code, r.stdout, r.stderr)
	}
	mustContain(t, r.stderr, "job job-1 queued")
	if _, err := os.Stat("-"); err == nil {
		os.Remove("-")
		t.Fatal("a literal file named '-' was created")
	}
}

func TestOutputDirectoryHandling(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	want := filepath.Join(dir, "job-1.pdf")
	if b, err := os.ReadFile(want); err != nil || string(b) != apitest.PDF {
		t.Fatalf("expected %s to be written: err=%v", want, err)
	}
	mustContain(t, r.stderr, "saved "+want)
	mustContain(t, r.stderr, "3 pages")

	nested := filepath.Join(t.TempDir(), "nested", "pdfs")
	if r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", nested); r.code != exitOK {
		t.Fatalf("nested dir: exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(nested, "job-1.pdf")); err != nil {
		t.Fatalf("expected job-1.pdf in the created directory: %v", err)
	}
}

func TestFailedDownloadKeepsPreviousFileAndLeavesNoTemp(t *testing.T) {
	f := apitest.New(t)
	f.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
		w.Write([]byte(`{"type":"https://docs.pdfik.net/error-codes#file-expired","title":"Gone","status":410,"detail":"expired"}`))
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "keep.pdf")
	if err := os.WriteFile(out, []byte("%PDF-yesterday"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", dir, "-f", "keep.pdf")
	if r.code != exitFailure {
		t.Fatalf("expected exit %d, got %d: %s", exitFailure, r.code, r.stderr)
	}
	if b, _ := os.ReadFile(out); string(b) != "%PDF-yesterday" {
		t.Fatalf("pre-existing output was damaged: %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "keep.pdf.partial-*")); len(m) != 0 {
		t.Fatalf("temp file leaked: %v", m)
	}
	mustContain(t, r.stderr, "API error 410")
}

func TestRenameFailureKeepsThePaidForPDF(t *testing.T) {
	orig := renameFile
	renameFile = func(string, string) error { return os.ErrPermission }
	t.Cleanup(func() { renameFile = orig })
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "download", "job-1", "-o", dir, "-f", "x.pdf")
	if r.code != exitFailure {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	mustContain(t, r.stderr, "The PDF was kept at")
	m, _ := filepath.Glob(filepath.Join(dir, "x.pdf.partial-*"))
	if len(m) != 1 {
		t.Fatalf("expected the partial file to be kept, found %v", m)
	}
	if b, _ := os.ReadFile(m[0]); string(b) != apitest.PDF {
		t.Fatalf("kept file has wrong content: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.pdf")); err == nil {
		t.Fatal("x.pdf must not exist when the rename failed")
	}
}

func TestHTMLToPDFSubmitsFileContents(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.html")
	os.WriteFile(in, []byte("\xEF\xBB\xBF<p>hi</p>"), 0o644) // with a UTF-8 BOM
	r := execute(t, f, nil, "html-to-pdf", in, "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if f.LastBody(t)["html"] != "<p>hi</p>" {
		t.Fatalf("html body wrong: %q", f.LastBody(t)["html"])
	}
	mustContain(t, r.stderr, "sanitized")

	r = execute(t, f, strings.NewReader("<b>stdin</b>"), "html-to-pdf", "-", "-o", dir)
	if r.code != exitOK || f.LastBody(t)["html"] != "<b>stdin</b>" {
		t.Fatalf("stdin input: exit %d body=%v", r.code, f.LastBody(t)["html"])
	}

	r = execute(t, f, nil, "html-to-pdf", filepath.Join(dir, "missing.html"))
	if r.code != exitUsage || f.Submits() != 2 {
		t.Fatalf("a missing file must be a usage error before any submit: %d %s", r.code, r.stderr)
	}
}

func TestReadInputEncodings(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	utf16le := []byte{0xFF, 0xFE}
	for _, r := range "<p>é</p>" {
		utf16le = append(utf16le, byte(r), 0x00)
	}
	utf16be := []byte{0xFE, 0xFF, 0x00, '<', 0x00, 'b', 0x00, '>'}
	cases := []struct {
		name    string
		path    string
		want    string
		wantErr string
	}{
		{"utf-16le with BOM (PowerShell 5.1)", write("le.html", utf16le), "<p>é</p>", ""},
		{"utf-16be with BOM", write("be.html", utf16be), "<b>", ""},
		{"utf-8 BOM stripped", write("bom.html", []byte("\xEF\xBB\xBF<p>x</p>")), "<p>x</p>", ""},
		{"odd-length utf-16", write("odd.html", []byte{0xFF, 0xFE, 0x3C}), "", "odd byte length"},
		{"latin-1 refused", write("latin1.html", []byte("<p>\xe9</p>")), "", "not valid UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readInput(tc.path, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q (%v), want %q", got, err, tc.want)
			}
		})
	}
	if _, err := readInput("-", strings.NewReader("<p>\xe9</p>")); err == nil || !strings.HasPrefix(err.Error(), "stdin") {
		t.Fatalf("invalid stdin must be named 'stdin': %v", err)
	}
}

func TestQuietSilencesProgressButNotWarnings(t *testing.T) {
	f := apitest.New(t)
	r := execute(t, f, nil, "html-to-pdf", "-", "-q", "-o", t.TempDir())
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if r.stderr != "" {
		t.Fatalf("-q must silence progress lines, got %q", r.stderr)
	}
	r = execute(t, f, nil, "download", "job-1", "--quiet", "-o", t.TempDir())
	if r.code != exitOK || r.stderr != "" {
		t.Fatalf("download --quiet: %d %q", r.code, r.stderr)
	}
}

func TestServerSideRenderFailureExitsThree(t *testing.T) {
	f := apitest.New(t)
	f.FinalStatus = map[string]any{"status": "failed", "error_code": "PAGE_TIMEOUT"}
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", dir)
	if r.code != exitRenderFailed {
		t.Fatalf("exit %d, want %d: %s", r.code, exitRenderFailed, r.stderr)
	}
	mustContain(t, r.stderr, "rendering failed (PAGE_TIMEOUT)")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("nothing must be written for a failed job, found %v", entries)
	}
}

func TestTimeoutExitsFourWithTheJobID(t *testing.T) {
	f := apitest.New(t)
	f.PollsUntilDone = 100
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--timeout", "1ms", "-o", t.TempDir())
	if r.code != exitNotFinished {
		t.Fatalf("exit %d, want %d: %s", r.code, exitNotFinished, r.stderr)
	}
	mustContain(t, r.stderr, "pdfik download job-1")
}

func TestRecoveryHintSurvivesAnAPIError(t *testing.T) {
	f := apitest.New(t)
	f.PollStatuses = []int{502, 502, 502}
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", t.TempDir())
	if r.code != exitFailure {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	mustContain(t, r.stderr, "API error 502")
	mustContain(t, r.stderr, "pdfik status job-1")
}

func TestInterruptedRunCleansUpAndHints(t *testing.T) {
	f := apitest.New(t)
	started := make(chan struct{})
	f.DownloadHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("%PDF"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	dir := t.TempDir()
	r := executeCtx(t, ctx, f, nil, "url-to-pdf", "https://example.com", "-o", dir, "-f", "x.pdf")
	if r.code != exitInterrupted {
		t.Fatalf("exit %d, want %d: %s", r.code, exitInterrupted, r.stderr)
	}
	mustContain(t, r.stderr, "pdfik download job-1")
	if m, _ := filepath.Glob(filepath.Join(dir, "*.partial-*")); len(m) != 0 {
		t.Fatalf("partial file left behind: %v", m)
	}
}

func TestStatusCommand(t *testing.T) {
	f := apitest.New(t)
	r := execute(t, f, nil, "status", "job-1")
	if r.code != exitOK || r.stdout != "status: done, download expires 2026-08-23T10:00:00Z\n" {
		t.Fatalf("status: %d %q %q", r.code, r.stdout, r.stderr)
	}
	f.FinalStatus = map[string]any{"status": "failed", "error_code": "PAGE_TIMEOUT", "test": true}
	if r := execute(t, f, nil, "status", "job-1"); r.stdout != "status: failed (test) (PAGE_TIMEOUT)\n" {
		t.Fatalf("status with code: %q", r.stdout)
	}
	if r := execute(t, f, nil, "status"); r.code != exitUsage || !strings.Contains(r.stderr, "exactly one job id") {
		t.Fatalf("no id: %d %q", r.code, r.stderr)
	}
	if r := execute(t, f, nil, "status", "../x"); r.code != exitUsage {
		t.Fatalf("malformed id must be refused before any request: %d %q", r.code, r.stderr)
	}
	if r := execute(t, f, nil, "status", "job-404"); r.code != exitFailure || !strings.Contains(r.stderr, "API error 404") {
		t.Fatalf("unknown id: %d %q", r.code, r.stderr)
	}
}

func TestDownloadChecksTheStatusFirst(t *testing.T) {
	f := apitest.New(t)
	f.PollsUntilDone = 100
	r := execute(t, f, nil, "download", "job-1", "-o", t.TempDir())
	if r.code != exitNotFinished || f.Downloads() != 0 {
		t.Fatalf("unfinished job: exit %d downloads=%d: %s", r.code, f.Downloads(), r.stderr)
	}
	mustContain(t, r.stderr, "pdfik status job-1")

	f2 := apitest.New(t)
	f2.FinalStatus = map[string]any{"status": "failed", "error_code": "SSRF_BLOCKED"}
	r = execute(t, f2, nil, "download", "job-1", "-o", t.TempDir())
	if r.code != exitRenderFailed || f2.Downloads() != 0 || !strings.Contains(r.stderr, "SSRF_BLOCKED") {
		t.Fatalf("failed job: exit %d downloads=%d: %s", r.code, f2.Downloads(), r.stderr)
	}

	f3 := apitest.New(t)
	dir := t.TempDir()
	r = execute(t, f3, nil, "download", "job-1", "--output", dir, "--file-name", "dl.pdf")
	if r.code != exitOK {
		t.Fatalf("download: %d %s", r.code, r.stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "dl.pdf")); string(b) != apitest.PDF {
		t.Fatalf("downloaded file wrong: %q", b)
	}
	r = execute(t, f3, nil, "download", "job-1", "-o", "-")
	if r.code != exitOK || r.stdout != apitest.PDF {
		t.Fatalf("download to stdout: %d %q", r.code, r.stdout)
	}
}

func TestEnvironmentFallbacks(t *testing.T) {
	f := apitest.New(t)
	t.Setenv("PDFIK_API_KEY", "sk_live_env")
	t.Setenv("PDFIK_API_URL", f.URL)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"url-to-pdf", "https://example.com", "-o", t.TempDir()},
		streams{in: strings.NewReader(""), out: &stdout, err: &stderr})
	if code != exitOK {
		t.Fatalf("env fallbacks: exit %d: %s", code, stderr.String())
	}
	if got := f.SubmitHeaders()[0].Get("X-API-Key"); got != "sk_live_env" {
		t.Fatalf("key from the environment not used: %q", got)
	}
	// The flag wins over the environment.
	code = run(context.Background(), []string{"url-to-pdf", "https://example.com", "-o", t.TempDir(), "--api-key", "sk_live_flag"},
		streams{in: strings.NewReader(""), out: &stdout, err: &stderr})
	if code != exitOK || f.SubmitHeaders()[1].Get("X-API-Key") != "sk_live_flag" {
		t.Fatalf("flag must win over env: %d %q", code, f.SubmitHeaders()[1].Get("X-API-Key"))
	}
}

func TestInsecureHTTPEscapeHatchWarnsOnce(t *testing.T) {
	t.Setenv("PDFIK_INSECURE_HTTP", "1")
	var stderr bytes.Buffer
	c, err := connFlags{apiKey: "k", apiURL: "http://lab.internal:8080"}.newClient(&stderr)
	if err != nil || c == nil {
		t.Fatalf("escape hatch must allow plain http: %v", err)
	}
	if n := strings.Count(stderr.String(), "plain http"); n != 1 {
		t.Fatalf("expected exactly one warning, got %d: %q", n, stderr.String())
	}
}

func TestAPIErrorsAreReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
		w.Write([]byte("{\"title\":\"Too Many Requests\",\"status\":429,\"error\":\"RATE_LIMIT_EXCEEDED\",\"detail\":\"slow\\u001b[2Jdown\"}"))
	}))
	defer srv.Close()
	r := execute(t, nil, nil, "url-to-pdf", "https://example.com", "--api-key", "sk_live_test", "--api-url", srv.URL)
	if r.code != exitFailure {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	mustContain(t, r.stderr, "API error 429 (RATE_LIMIT_EXCEEDED): slow[2Jdown")
	mustContain(t, r.stderr, "retry after: 5")
	if strings.Contains(r.stderr, "\x1b") {
		t.Fatalf("control characters reached the terminal: %q", r.stderr)
	}
}

func TestSavedSummaryShowsUsefulFacts(t *testing.T) {
	pages, size, total, load := 3, int64(45_678), 1850, 920
	st := api.JobStatus{PagesCount: &pages, ExpiresAt: "2026-08-21T10:00:00Z",
		Metrics: &api.Metrics{FileSizeBytes: &size, TotalDurationMs: &total, PageLoadMs: &load}}
	got := savedSummary(st, "", 4200*time.Millisecond)
	for _, want := range []string{"3 pages", "44.6 KB", "render 1.9s", "page load 920ms", "total 4.2s", "expires 2026-08-21T10:00:00Z"} {
		mustContain(t, got, want)
	}
	if savedSummary(api.JobStatus{}, "", 0) != "" {
		t.Fatal("empty status must produce an empty suffix")
	}
}

func TestMarginShorthand(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{"10mm,1cm,0.5in,20px", map[string]string{"top": "10mm", "right": "1cm", "bottom": "0.5in", "left": "20px"}},
		{"5mm 2cm", map[string]string{"top": "5mm", "right": "2cm", "bottom": "5mm", "left": "2cm"}},
		{"1in,2in,3in", map[string]string{"top": "1in", "right": "2in", "bottom": "3in", "left": "2in"}},
		{".5in", map[string]string{"top": ".5in", "right": ".5in", "bottom": ".5in", "left": ".5in"}},
	}
	for _, tc := range cases {
		got, err := buildMargin(tc.in, [4]string{})
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Fatalf("%q: %s = %q, want %q", tc.in, k, got[k], v)
			}
		}
	}
	for _, bad := range []string{"10em", "10mm,", ",10mm", "a,b,c,d,e", "10 mm", "10%", "10", "-5mm"} {
		if _, err := buildMargin(bad, [4]string{}); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	if m, err := buildMargin("", [4]string{"", "", "", "2cm"}); err != nil || len(m) != 1 || m["left"] != "2cm" {
		t.Fatalf("single side: %v %v", m, err)
	}
	if m, err := buildMargin("", [4]string{}); err != nil || m != nil {
		t.Fatalf("nothing set must give nil: %v %v", m, err)
	}
}

func TestParseInterleavedHonoursDoubleDash(t *testing.T) {
	fs := newFlagSet("x")
	test := fs.Bool("test", false, "")
	rest, err := parseInterleaved(fs, []string{"--", "https://example.com", "--test"})
	if err != nil || *test || len(rest) != 2 {
		t.Fatalf("tokens after -- must stay positional: rest=%v test=%v err=%v", rest, *test, err)
	}
}

func TestOutputPathHelpers(t *testing.T) {
	if p := outputPath("", "", "job-1"); p != filepath.Join(".", "job-1.pdf") {
		t.Fatalf("default path: %q", p)
	}
	if p := outputPath("out", "x.pdf", "job-1"); p != filepath.Join("out", "x.pdf") {
		t.Fatalf("named path: %q", p)
	}
	if p := outputPath("-", "ignored.pdf", "job-1"); p != "-" {
		t.Fatalf("stdout marker: %q", p)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	os.WriteFile(file, []byte("x"), 0o644)
	if err := prepareOutputDir(file, "-o/--output"); err == nil || !strings.Contains(err.Error(), "is a file") {
		t.Fatalf("file as directory: %v", err)
	}
	if err := checkFileName("a\\b.pdf"); err == nil {
		t.Fatal("backslash in -f must be refused")
	}
}

func TestQuietFailuresStillNameTheJob(t *testing.T) {
	f := apitest.New(t)
	f.FinalStatus = map[string]any{"status": "failed", "error_code": "PAGE_TIMEOUT"}
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-q", "-o", t.TempDir())
	if r.code != exitRenderFailed {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	mustContain(t, r.stderr, "job-1")
}

func TestDownloadOfUnfinishedJobReadsNaturally(t *testing.T) {
	f := apitest.New(t)
	f.PollsUntilDone = 100
	r := execute(t, f, nil, "download", "job-1", "-o", t.TempDir())
	if r.code != exitNotFinished || strings.Contains(r.stderr, "after 0s") {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	mustContain(t, r.stderr, "is still rendering")
}

func TestStdoutGuardRefusesBeforeSubmitAndAllowsDevNull(t *testing.T) {
	f := apitest.New(t)
	orig := stdoutIsTerminal
	stdoutIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = orig })
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", "-")
	if r.code != exitUsage || f.Submits() != 0 {
		t.Fatalf("a terminal on stdout must be refused before anything is submitted: exit %d submits=%d: %s", r.code, f.Submits(), r.stderr)
	}
	r = execute(t, f, nil, "download", "job-1", "-o", "-")
	if r.code != exitUsage || f.Downloads() != 0 {
		t.Fatalf("download to a terminal: exit %d downloads=%d", r.code, f.Downloads())
	}
	stdoutIsTerminal = orig

	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skip(err)
	}
	defer null.Close()
	if isTerminal(null) {
		t.Fatalf("%s must not count as a terminal", os.DevNull)
	}
	if isTerminal(new(bytes.Buffer)) {
		t.Fatal("a buffer is not a terminal")
	}
}

func TestExitCodesAreDocumented(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var usageText bytes.Buffer
	usage(&usageText)
	for _, code := range []int{exitOK, exitFailure, exitUsage, exitRenderFailed, exitNotFinished, exitInterrupted} {
		row := "| `" + strconv.Itoa(code) + "` |"
		if !strings.Contains(string(readme), row) {
			t.Errorf("README.md lacks the exit-code row for %d", code)
		}
		if !regexp.MustCompile(`(^|\s)` + strconv.Itoa(code) + `\s`).MatchString(usageText.String()) {
			t.Errorf("usage() lacks exit code %d", code)
		}
	}
}
