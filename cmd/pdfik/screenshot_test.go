package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfik/cli/internal/apitest"
)

func TestURLToImageDefaultsStayTheAPIsBusiness(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-image", "https://example.com", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "job-1.png")); err != nil || string(b) != apitest.PDF {
		t.Fatalf("expected job-1.png with the download body: %v %q", err, b)
	}
	body := f.LastBody(t)
	if body["url"] != "https://example.com" {
		t.Fatalf("url body wrong: %v", body)
	}
	for _, key := range []string{"options", "test", "delivery", "webhook_url"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not be sent unless set: %v", key, body)
		}
	}
}

func TestURLToImageSendsScreenshotOptions(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-image", "https://example.com", "--format", "jpeg",
		"--quality", "80", "--full-page", "--viewport", "800x600", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "job-1.jpg")); err != nil {
		t.Fatalf("jpeg output must default to .jpg: %v", err)
	}
	opts, _ := f.LastBody(t)["options"].(map[string]any)
	if opts["format"] != "jpeg" || opts["quality"] != float64(80) || opts["full_page"] != true {
		t.Fatalf("unexpected options: %v", opts)
	}
	vp, _ := opts["viewport"].(map[string]any)
	if vp["width"] != float64(800) || vp["height"] != float64(600) {
		t.Fatalf("unexpected viewport: %v", vp)
	}
}

func TestURLToImageJpgAliasAndFullPageDefault(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-image", "https://example.com", "--format", "jpg", "-o", dir, "-f", "shot.jpg")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "shot.jpg")); err != nil {
		t.Fatalf("output not written: %v", err)
	}
	opts, _ := f.LastBody(t)["options"].(map[string]any)
	if opts["format"] != "jpeg" {
		t.Fatalf("jpg must be sent as jpeg: %v", opts)
	}
	if _, ok := opts["full_page"]; ok {
		t.Fatalf("full_page must not be sent unless --full-page is given: %v", opts)
	}
}

// A viewport may be far taller than it is wide — the two axes have different
// ceilings (1920 vs 8192), and a single shared bound used to reject this.
func TestURLToImageAcceptsTallViewport(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "url-to-image", "https://example.com", "--viewport", "1280x8192", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	opts, _ := f.LastBody(t)["options"].(map[string]any)
	vp, _ := opts["viewport"].(map[string]any)
	if vp["width"] != float64(1280) || vp["height"] != float64(8192) {
		t.Fatalf("unexpected viewport: %v", vp)
	}
}

func TestHTMLToImageReadsFileAndStdin(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.html")
	if err := os.WriteFile(in, []byte("<p>shot</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := execute(t, f, nil, "html-to-image", in, "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if f.LastBody(t)["html"] != "<p>shot</p>" {
		t.Fatalf("html body wrong: %q", f.LastBody(t)["html"])
	}
	mustContain(t, r.stderr, "sanitized")

	r = execute(t, f, strings.NewReader("<b>stdin</b>"), "html-to-image", "-", "--test", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("stdin input: exit %d: %s", r.code, r.stderr)
	}
	body := f.LastBody(t)
	if body["html"] != "<b>stdin</b>" || body["test"] != true {
		t.Fatalf("unexpected body: %v", body)
	}
	mustContain(t, r.stderr, "sample image")
}

func TestScreenshotValidationIsAUsageError(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown image format", []string{"url-to-image", "https://example.com", "--format", "gif"}, "png, jpeg"},
		{"quality with png", []string{"url-to-image", "https://example.com", "--quality", "80"}, "jpeg only"},
		{"quality zero", []string{"url-to-image", "https://example.com", "--format", "jpeg", "--quality", "0"}, "between 1 and 100"},
		{"quality too high", []string{"url-to-image", "https://example.com", "--format", "jpeg", "--quality", "101"}, "between 1 and 100"},
		{"quality not a number", []string{"url-to-image", "https://example.com", "--format", "jpeg", "--quality", "high"}, "between 1 and 100"},
		{"viewport missing height", []string{"url-to-image", "https://example.com", "--viewport", "1280"}, "WIDTHxHEIGHT"},
		{"viewport below minimum", []string{"url-to-image", "https://example.com", "--viewport", "100x700"}, "width 320-1920"},
		{"viewport wider than the maximum", []string{"url-to-image", "https://example.com", "--viewport", "1921x700"}, "width 320-1920"},
		{"viewport taller than the maximum", []string{"url-to-image", "https://example.com", "--viewport", "1280x8193"}, "height 320-8192"},
		{"viewport not numeric", []string{"url-to-image", "https://example.com", "--viewport", "widexhigh"}, "WIDTHxHEIGHT"},
		{"page flag refused", []string{"url-to-image", "https://example.com", "--landscape"}, "landscape"},
		{"-o that names an image file", []string{"url-to-image", "https://example.com", "-o", "shot.png"}, "--file-name"},
		{"output dir with deliver-url", []string{"url-to-image", "https://example.com", "--deliver-url", "https://b.example/k?sig=1", "-o", "out"}, "--deliver-url"},
		{"stdout with deliver-url", []string{"html-to-image", "page.html", "--deliver-url", "https://b.example/k", "-o", "-"}, "--deliver-url"},
		{"two positionals", []string{"url-to-image", "https://a.example", "https://b.example"}, "exactly one input"},
		{"no positional", []string{"html-to-image"}, "exactly one input"},
		{"missing file", []string{"html-to-image", "missing.html", "--api-key", "k"}, "missing.html"},
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
