package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfik/cli/internal/apitest"
)

const testMarkdown = "# Title\n\nSome **bold** text.\n"

func writeMarkdown(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(p, []byte(testMarkdown), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMarkdownSubmitsContentAndPageOptions(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	in := writeMarkdown(t, dir)
	r := execute(t, f, nil, "markdown-to-pdf", in, "--format", "A3", "--landscape",
		"--margin", "10mm", "--no-background", "-o", dir, "-f", "doc.pdf")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.pdf")); err != nil {
		t.Fatalf("output not written: %v", err)
	}
	body := f.LastBody(t)
	if body["markdown"] != testMarkdown {
		t.Fatalf("markdown body wrong: %q", body["markdown"])
	}
	opts, _ := body["options"].(map[string]any)
	if opts["format"] != "A3" || opts["landscape"] != true || opts["print_background"] != false {
		t.Fatalf("unexpected options: %v", opts)
	}
	m, _ := opts["margin"].(map[string]any)
	if m["top"] != "10mm" || m["left"] != "10mm" {
		t.Fatalf("unexpected margin: %v", m)
	}
}

func TestMarkdownDefaultsStayTheAPIsBusiness(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "markdown-to-pdf", writeMarkdown(t, dir), "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "job-1.pdf")); err != nil {
		t.Fatalf("default output name must be job-1.pdf: %v", err)
	}
	body := f.LastBody(t)
	for _, key := range []string{"options", "test", "delivery", "webhook_url"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not be sent unless set: %v", key, body)
		}
	}
}

func TestMarkdownReadsStdinAndSendsTest(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, strings.NewReader(testMarkdown), "markdown-to-pdf", "-", "--test", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	body := f.LastBody(t)
	if body["markdown"] != testMarkdown || body["test"] != true {
		t.Fatalf("unexpected body: %v", body)
	}
	mustContain(t, r.stderr, "sample PDF")
}

func TestMarkdownValidationIsAUsageError(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown paper format", []string{"markdown-to-pdf", "doc.md", "--format", "B5"}, "Ledger"},
		{"image flag refused", []string{"markdown-to-pdf", "doc.md", "--quality", "80"}, "quality"},
		{"margin without unit", []string{"markdown-to-pdf", "doc.md", "--margin", "10"}, "not a valid length"},
		{"two positionals", []string{"markdown-to-pdf", "a.md", "b.md"}, "exactly one input"},
		{"no positional", []string{"markdown-to-pdf"}, "exactly one input"},
		{"missing file", []string{"markdown-to-pdf", "missing.md", "--api-key", "k"}, "missing.md"},
		{"file name with deliver-url", []string{"markdown-to-pdf", "doc.md", "--deliver-url", "https://b.example/k", "-f", "x.pdf"}, "--deliver-url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := execute(t, nil, nil, tc.args...)
			if r.code != exitUsage {
				t.Fatalf("exit %d, want %d: %s", r.code, exitUsage, r.stderr)
			}
			mustContain(t, r.stderr, tc.want)
		})
	}
}

// The --deliver-url pipeline is shared by every rendering command except
// einvoice-to-pdf: the output goes straight to the caller's own storage, the
// download step is skipped entirely (the API answers 404 for such jobs), and
// the delivered location — the presigned URL minus its query string — is
// printed with the job id.
func TestDeliverURLSkipsTheDownload(t *testing.T) {
	const presigned = "https://bucket.example.com/renders/out.bin?X-Amz-Signature=abc123&X-Amz-Expires=900"
	const delivered = "https://bucket.example.com/renders/out.bin"
	cases := []struct {
		cmd   string
		input string
		stdin string
	}{
		{"url-to-pdf", "https://example.com", ""},
		{"html-to-pdf", "-", "<p>x</p>"},
		{"markdown-to-pdf", "-", testMarkdown},
		{"url-to-image", "https://example.com", ""},
		{"html-to-image", "-", "<p>x</p>"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			f := apitest.New(t)
			// Delivered jobs finish with no expires_at and nothing stored on
			// the API's side — the status alone must be enough for the CLI.
			f.FinalStatus = map[string]any{"status": "done"}
			var stdin io.Reader
			if tc.stdin != "" {
				stdin = strings.NewReader(tc.stdin)
			}
			r := execute(t, f, stdin, tc.cmd, tc.input, "--deliver-url", presigned)
			if r.code != exitOK {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			if f.Downloads() != 0 {
				t.Fatalf("a delivered job must never be downloaded, got %d downloads", f.Downloads())
			}
			mustContain(t, r.stderr, "job job-1 delivered: "+delivered)
			if strings.Contains(r.stderr, "X-Amz-Signature") {
				t.Fatalf("the presigned query string must not be printed:\n%s", r.stderr)
			}
			d, _ := f.LastBody(t)["delivery"].(map[string]any)
			if d["mode"] != "presigned_put" || d["url"] != presigned {
				t.Fatalf("unexpected delivery object: %v", f.LastBody(t))
			}
			for _, leftover := range []string{"job-1.pdf", "job-1.png", "job-1.jpg"} {
				if _, err := os.Stat(leftover); err == nil {
					os.Remove(leftover)
					t.Fatalf("no local file must be written for a delivered job, found %s", leftover)
				}
			}
			if r.stdout != "" {
				t.Fatalf("stdout must stay clean: %q", r.stdout)
			}
		})
	}
}

func TestDeliverURLWithTestSurfacesTheServersRefusal(t *testing.T) {
	f := apitest.New(t)
	f.SubmitStatuses = []int{400}
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "--test",
		"--deliver-url", "https://bucket.example.com/renders/out.pdf?sig=1")
	if r.code != exitFailure {
		t.Fatalf("exit %d, want %d: %s", r.code, exitFailure, r.stderr)
	}
	mustContain(t, r.stderr, "API error 400")
	body := f.LastBody(t)
	if body["test"] != true {
		t.Fatalf("test must still be sent for the server to refuse: %v", body)
	}
	if _, ok := body["delivery"]; !ok {
		t.Fatalf("delivery must still be sent for the server to refuse: %v", body)
	}
	if f.Polls() != 0 || f.Downloads() != 0 {
		t.Fatalf("nothing must be polled or downloaded after a refused submit: polls=%d downloads=%d", f.Polls(), f.Downloads())
	}
}

func TestQuietSilencesTheDeliveredLine(t *testing.T) {
	f := apitest.New(t)
	f.FinalStatus = map[string]any{"status": "done"}
	r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-q",
		"--deliver-url", "https://bucket.example.com/renders/out.pdf?sig=1")
	if r.code != exitOK || r.stderr != "" || r.stdout != "" {
		t.Fatalf("-q must silence the delivered line: %d stderr=%q stdout=%q", r.code, r.stderr, r.stdout)
	}
	if f.Downloads() != 0 {
		t.Fatalf("a delivered job must never be downloaded, got %d downloads", f.Downloads())
	}
}
