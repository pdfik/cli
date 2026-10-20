package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfik/cli/internal/apitest"
)

func TestSyncWritesTheHeaderNamedFileAtomically(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--sync", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	// The default name comes from the X-PDFik-Job-Id response header — the
	// only place a sync run learns its job id.
	want := filepath.Join(dir, "job-1.pdf")
	if b, err := os.ReadFile(want); err != nil || string(b) != apitest.PDF {
		t.Fatalf("expected %s with the file bytes: err=%v body=%q", want, err, b)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.partial-*")); len(m) != 0 {
		t.Fatalf("temp file leaked: %v", m)
	}
	if f.Submits() != 1 || f.Polls() != 0 || f.Downloads() != 0 {
		t.Fatalf("sync must be one request: submits=%d polls=%d downloads=%d", f.Submits(), f.Polls(), f.Downloads())
	}
	if f.LastBody(t)["wait"] != true {
		t.Fatalf("wait not sent: %v", f.LastBody(t))
	}
	mustContain(t, r.stderr, "saved "+want)
	mustContain(t, r.stderr, "job-1")

	// -f stays the user's choice, exactly as in the async flow.
	r = execute(t, f, nil, "url-to-pdf", "https://example.com", "--sync", "-o", dir, "-f", "named.pdf")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "named.pdf")); err != nil || string(b) != apitest.PDF {
		t.Fatalf("-f must be honoured: err=%v body=%q", err, b)
	}
}

func TestSyncStreamsToStdout(t *testing.T) {
	f := apitest.New(t)
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--sync", "-o", "-")
	if r.code != exitOK || r.stdout != apitest.PDF {
		t.Fatalf("stdout must carry exactly the file: code=%d stdout=%q stderr=%q", r.code, r.stdout, r.stderr)
	}
	if f.Downloads() != 0 {
		t.Fatalf("no download step in sync mode, got %d", f.Downloads())
	}
}

func TestSyncWithDeliverURLIsRefusedLocally(t *testing.T) {
	f := apitest.New(t)
	for _, args := range [][]string{
		{"url-to-pdf", "https://example.com", "--sync", "--deliver-url", "https://b.example/k?sig=1"},
		{"markdown-to-pdf", "-", "--sync", "--deliver-url", "https://b.example/k"},
		{"url-to-image", "https://example.com", "--sync", "--deliver-url", "https://b.example/k"},
	} {
		r := execute(t, f, strings.NewReader("# md"), args...)
		if r.code != exitUsage {
			t.Fatalf("%v: exit %d, want %d: %s", args, r.code, exitUsage, r.stderr)
		}
		mustContain(t, r.stderr, "--sync")
		mustContain(t, r.stderr, "--deliver-url")
	}
	if f.Submits() != 0 {
		t.Fatalf("a local refusal must not submit anything, got %d", f.Submits())
	}
}

func TestSyncTimeoutExitsFiveWithoutDownloadAdvice(t *testing.T) {
	f := apitest.New(t)
	f.SyncHandling = "timeout"
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--sync", "-o", dir)
	if r.code != exitSyncTimeout {
		t.Fatalf("exit %d, want %d: %s", r.code, exitSyncTimeout, r.stderr)
	}
	mustContain(t, r.stderr, "nothing was charged")
	mustContain(t, r.stderr, "without --sync")
	// The job was cancelled server-side: there is nothing to download, so the
	// usual recovery advice would only lead to a 404.
	if strings.Contains(r.stderr, "pdfik download") {
		t.Fatalf("a sync timeout must not advise pdfik download:\n%s", r.stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("nothing must be written on a timeout, found %v", entries)
	}
}

func TestSyncRenderFailureExitsThree(t *testing.T) {
	f := apitest.New(t)
	f.SyncHandling = "failed"
	f.SyncErrorCode = "PAGE_TIMEOUT"
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--sync", "-o", dir)
	if r.code != exitRenderFailed {
		t.Fatalf("exit %d, want %d: %s", r.code, exitRenderFailed, r.stderr)
	}
	mustContain(t, r.stderr, "rendering failed (PAGE_TIMEOUT)")
	mustContain(t, r.stderr, "job-1")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("nothing must be written for a failed job, found %v", entries)
	}
}

func TestSyncImageLandsUnderTheImageExtension(t *testing.T) {
	f := apitest.New(t)
	f.SyncContentType = "image/png"
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-image", "https://example.com", "--sync", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "job-1.png")); err != nil || string(b) != apitest.PNG {
		t.Fatalf("expected job-1.png with the PNG bytes: err=%v body=%q", err, b)
	}
}

func TestSyncWarningHeaderPrintsEvenWhenQuiet(t *testing.T) {
	f := apitest.New(t)
	f.SyncWarning = "profile minimum embeds accompanying data only — the result is NOT a legally sufficient e-invoice"
	dir := t.TempDir()
	r := execute(t, f, strings.NewReader(testInvoiceXML), "einvoice-to-pdf", "-", "--sync", "-q", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	// -q silences progress; a server warning is a diagnostic and still prints.
	mustContain(t, r.stderr, "pdfik: warning: profile minimum")
	if strings.Contains(r.stderr, "saved ") {
		t.Fatalf("-q must silence the progress lines:\n%s", r.stderr)
	}
	if f.LastBody(t)["wait"] != true || f.LastBody(t)["xml"] != testInvoiceXML {
		t.Fatalf("unexpected body: %v", f.LastBody(t))
	}
}
