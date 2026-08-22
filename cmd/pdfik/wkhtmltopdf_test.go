package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pdfik/cli/internal/apitest"
)

// buildBody runs only the parse+build pipeline — no network — and returns the
// canonical JSON the mode would submit. The golden tests below pin the mapping:
// if a flag silently changes meaning, a diff here catches it.
func buildBody(t *testing.T, args ...string) string {
	t.Helper()
	st := newWkState()
	st.inputURL, st.inputIsURL = "https://example.com", true // the golden cases model URL input
	if err := parseWkArgs(st, args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	body, err := st.buildSubmission()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Go maps marshal with sorted keys → stable golden strings. HTML escaping is
	// off so the fragments below can read like the template they check.
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(buf.String())
}

func wk(t *testing.T, f *apitest.Server, args ...string) result {
	t.Helper()
	return execute(t, f, nil, append([]string{"wkhtmltopdf"}, args...)...)
}

func TestWkDefaultsMatchWkhtmltopdf(t *testing.T) {
	got := buildBody(t)
	want := `{"options":{"margin":{"bottom":"10mm","left":"10mm","right":"10mm","top":"10mm"},"print_background":true}}`
	if got != want {
		t.Fatalf("defaults drifted:\n got %s\nwant %s", got, want)
	}
}

func TestWkGoldenMappings(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string // fragments that must appear in the JSON body
	}{
		{"page size and orientation", []string{"-s", "A3", "-O", "Landscape"}, []string{`"format":"A3"`, `"landscape":true`}},
		{"long flags with equals", []string{"--page-size=Letter", "--orientation=Portrait"}, []string{`"format":"Letter"`, `"landscape":false`}},
		{"bare margins get wkhtmltopdf's mm", []string{"-T", "20", "--margin-left", "1in"}, []string{`"top":"20mm"`, `"left":"1in"`}},
		{"pt margins are converted", []string{"-T", "10pt"}, []string{`"top":"3.528mm"`}},
		{"no-background", []string{"--no-background"}, []string{`"print_background":false`}},
		{"zoom maps to scale", []string{"--zoom", "1.5"}, []string{`"scale":1.5`}},
		{"disable-smart-shrinking pins scale to 1", []string{"--disable-smart-shrinking"}, []string{`"scale":1`}},
		{"explicit zoom wins over disable-smart-shrinking", []string{"--zoom", "1.5", "--disable-smart-shrinking"}, []string{`"scale":1.5`}},
		{"javascript delay maps to render wait", []string{"--javascript-delay", "2000"}, []string{`"render":{"wait_after_load_ms":2000}`}},
		{"image quality maps to compression", []string{"--image-quality", "80"}, []string{`"compression":{"image_quality":80}`}},
		{"footer with classic placeholders", []string{"--footer-center", "Page [page] of [topage]", "--footer-font-size", "9"},
			[]string{`"display_header_footer":true`, `class=\"pageNumber\"`, `class=\"totalPages\"`, `font-size:9pt`}},
		{"camel-case tokens as in the wkhtmltopdf docs", []string{"--footer-right", "[page]/[toPage]"}, []string{`class=\"totalPages\"`}},
		{"header line becomes a bottom border", []string{"--header-left", "[title]", "--header-line"}, []string{`border-bottom:1px solid`, `class=\"title\"`}},
		{"header font name maps to font-family", []string{"--header-center", "x", "--header-font-name", "Georgia"}, []string{`font-family:'Georgia', sans-serif`}},
		{"header padding follows the side margins", []string{"--header-center", "x", "-L", "25", "-R", "5"}, []string{`padding:0 5mm 0 25mm`}},
		{"layout uses sanitizer-allowed CSS only", []string{"--footer-center", "x"}, []string{`display:inline-block`, `width:33%`}},
		{"default header", []string{"--default-header"}, []string{`class=\"url\"`, `class=\"pageNumber\"`, `border-bottom`}},
		{"replace substitutes a custom token", []string{"--replace", "client", "ACME & Co", "--footer-left", "[client]"}, []string{`ACME &amp; Co`}},
		{"title feeds the title placeholder", []string{"--title", "Report", "--header-left", "[doctitle]"}, []string{`>Report<`}},
		{"frompage is always 1", []string{"--footer-left", "[frompage]"}, []string{`>1<`}},
		{"test extension", []string{"--test"}, []string{`"test":true`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildBody(t, tc.args...)
			for _, frag := range tc.want {
				if !strings.Contains(got, frag) {
					t.Fatalf("missing %s in body:\n%s", frag, got)
				}
			}
		})
	}
}

func TestWkTemplateNeverUsesFlex(t *testing.T) {
	got := buildBody(t, "--footer-center", "Page [page]", "--footer-right", "r")
	if strings.Contains(got, "flex") {
		t.Fatalf("template uses flex, which the server sanitizer strips:\n%s", got)
	}
}

func TestWkHeaderTextIsEscaped(t *testing.T) {
	var body struct {
		Options struct {
			FooterTemplate string `json:"footer_template"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(buildBody(t, "--footer-center", `A&B <i>x</i>`)), &body); err != nil {
		t.Fatal(err)
	}
	tpl := body.Options.FooterTemplate
	if strings.Contains(tpl, "<i>") || !strings.Contains(tpl, "A&amp;B") || !strings.Contains(tpl, "&lt;i&gt;") {
		t.Fatalf("escaping incomplete or markup injected:\n%s", tpl)
	}
}

func TestWkTimeTokensAreResolvedAtSubmission(t *testing.T) {
	orig := wkNow
	wkNow = func() time.Time { return time.Date(2026, 8, 22, 9, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { wkNow = orig })
	st := newWkState()
	if err := parseWkArgs(st, []string{"--footer-left", "[isodate] [time] [section]"}); err != nil {
		t.Fatal(err)
	}
	body, err := st.buildSubmission()
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := body["options"].(map[string]any)["footer_template"].(string)
	if !strings.Contains(tpl, "2026-08-22 09:30:00 [section]") {
		t.Fatalf("time tokens not resolved / section not literal: %s", tpl)
	}
	joined := strings.Join(st.warnings, "\n")
	if !strings.Contains(joined, "[section] has no equivalent") || !strings.Contains(joined, "resolved at submission time") {
		t.Fatalf("expected the placeholder warnings, got %v", st.warnings)
	}
}

func TestWkExtensionFlagsAcceptEqualsForm(t *testing.T) {
	st := newWkState()
	err := parseWkArgs(st, []string{"--api-key=sk_live_x", "--api-url=http://localhost:1", "--timeout=90s", "--test"})
	if err != nil {
		t.Fatalf("inline '=' forms must parse: %v", err)
	}
	if st.conn.apiKey != "sk_live_x" || st.conn.apiURL != "http://localhost:1" || st.timeout != 90*time.Second || !st.test {
		t.Fatalf("values lost: %+v", st)
	}
}

func TestWkInvalidValuesAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--timeout", "90"},
		{"-O", "Sideways"},
		{"-s", "B5"},
		{"--javascript-delay", "60000"},
		{"--image-quality", "0"},
		{"--zoom", "0.25"},
		{"--zoom", "NaN"},
		{"-T", "10em"},
		{"-T", "-5"},
		{"-T", "NaN"},
		{"--margin-left", "abc"},
		{"--header-font-size", "big"},
		{"--header-font-name", "x;}<b>"},
		{"--encoding", "latin1"},
		{"--replace", "name"}, // needs two values
	} {
		st := newWkState()
		err := parseWkArgs(st, args)
		if err == nil {
			t.Fatalf("%v: expected an error", args)
		}
		if code := report(new(strings.Builder), "pdfik wkhtmltopdf", err); code != exitUsage {
			t.Fatalf("%v: expected a usage error, got exit %d (%v)", args, code, err)
		}
	}
}

func TestWkUnsupportedFlagsRefuse(t *testing.T) {
	for _, args := range [][]string{
		{"--grayscale"}, {"-n"}, {"--disable-javascript"}, {"--cookie", "session", "abc"},
		{"--custom-header", "X-Auth", "secret"}, {"--page-width", "100mm"}, {"--no-print-media-type"},
		{"--outline"}, {"--user-style-sheet", "s.css"}, {"--run-script", "x.js"}, {"--no-images"},
		{"--page-offset", "3"}, {"--cookie-jar", "c.txt"}, {"toc"}, {"cover", "c.html"},
	} {
		st := newWkState()
		err := parseWkArgs(st, args)
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("%v: expected an honest refusal, got %v", args, err)
		}
	}
}

func TestWkIgnorableFlagsWarnAndContinue(t *testing.T) {
	st := newWkState()
	if err := parseWkArgs(st, []string{"--stop-slow-scripts", "-d", "300", "--copies", "2", "--no-outline", "--images", "in.html", "out.pdf"}); err != nil {
		t.Fatalf("ignorable flags must not fail: %v", err)
	}
	if len(st.warnings) != 5 {
		t.Fatalf("expected 5 warnings, got %v", st.warnings)
	}
	if len(st.positional) != 2 {
		t.Fatalf("positionals lost: %v", st.positional)
	}
}

func TestWkNoHeaderLineIsAccepted(t *testing.T) {
	st := newWkState()
	if err := parseWkArgs(st, []string{"--no-header-line", "--no-footer-line"}); err != nil {
		t.Fatalf("explicit defaults must parse: %v", err)
	}
	if _, err := st.buildSubmission(); err != nil {
		t.Fatal(err)
	}
	if _, has := st.options["header_template"]; has {
		t.Fatal("--no-header-line alone must not create a header")
	}
}

func TestWkUnknownFlagPointsAtTheDoc(t *testing.T) {
	st := newWkState()
	err := parseWkArgs(st, []string{"--totally-new-flag"})
	if err == nil || !strings.Contains(err.Error(), "COMPATIBILITY.md") {
		t.Fatalf("expected pointer to COMPATIBILITY.md, got %v", err)
	}
}

func TestWkVersionAndHelpProbes(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-V"}, {"https://example.com", "out.pdf", "--version"}} {
		r := wk(t, nil, args...)
		if r.code != exitOK || !strings.HasPrefix(r.stdout, "pdfik ") {
			t.Fatalf("%v: %d %q %q", args, r.code, r.stdout, r.stderr)
		}
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"-H"}, {"--extended-help"}, {"-s", "A4", "--help"}} {
		r := wk(t, nil, args...)
		if r.code != exitOK || !strings.Contains(r.stdout, "pdfik wkhtmltopdf [wkhtmltopdf-flags]") {
			t.Fatalf("%v: %d %q", args, r.code, r.stdout)
		}
	}
}

func TestWkHeaderHTMLFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "footer.html")
	if err := os.WriteFile(file, []byte(`<p>Page [page]/[topage] — [client]</p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newWkState()
	if err := parseWkArgs(st, []string{"--footer-html", file, "--replace", "client", "ACME"}); err != nil {
		t.Fatal(err)
	}
	body, err := st.buildSubmission()
	if err != nil {
		t.Fatal(err)
	}
	opts := body["options"].(map[string]any)
	tpl, _ := opts["footer_template"].(string)
	if !strings.Contains(tpl, `class="pageNumber"`) || !strings.Contains(tpl, `class="totalPages"`) || strings.Contains(tpl, "[page]") || !strings.Contains(tpl, "ACME") {
		t.Fatalf("tokens not substituted in the file: %s", tpl)
	}
	if opts["display_header_footer"] != true || opts["header_template"] != "<span></span>" {
		t.Fatalf("header must be blanked when only a footer file is set: %v", opts)
	}
	if !strings.Contains(strings.Join(st.warnings, "\n"), "basic markup") {
		t.Fatalf("expected the basic-markup warning, got %v", st.warnings)
	}

	// Refusals: the JS contract, oversized files, URLs, missing files.
	js := filepath.Join(dir, "header.html")
	os.WriteFile(js, []byte(`<html><body onload="subst()"><script>function subst(){}</script></body></html>`), 0o644)
	st = newWkState()
	st.header.htmlFile, st.header.used = js, true
	if _, err := st.buildSubmission(); err == nil || !strings.Contains(err.Error(), "JavaScript substitution") {
		t.Fatalf("classic JS header files must be refused, got %v", err)
	}
	big := filepath.Join(dir, "big.html")
	os.WriteFile(big, []byte("<div>"+strings.Repeat("a", 10001)+"</div>"), 0o644)
	st = newWkState()
	st.footer.htmlFile, st.footer.used = big, true
	if _, err := st.buildSubmission(); err == nil || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("oversized template must be refused with the API cap, got %v", err)
	}
	st = newWkState()
	st.header.htmlFile, st.header.used = "https://x/h.html", true
	if _, err := st.buildSubmission(); err == nil || !strings.Contains(err.Error(), "local file") {
		t.Fatalf("URL templates must be refused, got %v", err)
	}
	st = newWkState()
	st.header.htmlFile, st.header.used = filepath.Join(dir, "missing.html"), true
	if _, err := st.buildSubmission(); err == nil || !strings.Contains(err.Error(), "missing.html") {
		t.Fatalf("missing template file must name the path, got %v", err)
	}
}

func TestWkUsernameOnlyStillAuthenticates(t *testing.T) {
	// wkhtmltopdf sends basic auth with an empty password when only --username
	// is given (token-as-username schemes) — dropping it silently would render
	// the anonymous view.
	f := apitest.New(t)
	out := filepath.Join(t.TempDir(), "x.pdf")
	r := wk(t, f, "--username", "tok_abc", "-q", "https://example.com/private", out)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	auth, _ := f.LastBody(t)["auth"].(map[string]any)
	if auth == nil || auth["type"] != "basic" || auth["value"] != "tok_abc:" {
		t.Fatalf("auth lost or wrong: %v", f.LastBody(t)["auth"])
	}
}

func TestWkLocalFileInput(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "in.html")
	os.WriteFile(file, []byte("<h1>x</h1>"), 0o644)
	f := apitest.New(t)

	r := wk(t, f, "--username", "bob", file, filepath.Join(dir, "out.pdf"))
	if r.code != exitUsage || !strings.Contains(r.stderr, "URL input") {
		t.Fatalf("username with file input must be refused: code=%d, %s", r.code, r.stderr)
	}

	r = wk(t, f, file, filepath.Join(dir, "out.pdf"))
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	mustContain(t, r.stderr, "sanitizes HTML input")
	if f.LastBody(t)["html"] != "<h1>x</h1>" {
		t.Fatalf("file contents not submitted: %v", f.LastBody(t)["html"])
	}

	// file:// is a local path in wkhtmltopdf and stays one here.
	r = wk(t, f, "file://"+filepath.ToSlash(file), filepath.Join(dir, "out2.pdf"))
	if r.code != exitOK || f.Submits() != 2 {
		t.Fatalf("file:// input: exit %d submits=%d: %s", r.code, f.Submits(), r.stderr)
	}

	// A typo'd file name must report the OS error, not hijack it into a URL hint.
	r = wk(t, f, filepath.Join(dir, "invoce.html"), filepath.Join(dir, "out3.pdf"))
	if r.code != exitUsage || strings.Contains(r.stderr, "https://") || !strings.Contains(r.stderr, "invoce.html") {
		t.Fatalf("missing local file: code=%d %s", r.code, r.stderr)
	}

	// A schemeless host gets the hint.
	r = wk(t, nil, "example.com", filepath.Join(dir, "out4.pdf"))
	if r.code != exitUsage || !strings.Contains(r.stderr, "https://example.com") {
		t.Fatalf("schemeless URL: code=%d %s", r.code, r.stderr)
	}
}

func TestLooksHostLike(t *testing.T) {
	for in, want := range map[string]bool{
		"example.com": true, "localhost:8080/x": true, "api.example.com/path": true,
		`C:\reports\in.html`: false, "C:/x/in.html": false, "docs/in.html": false,
		"in.html": false, "index.htm": false, "./in.html": false, "../x.html": false, "-": false, "report": false,
	} {
		if got := looksHostLike(in); got != want {
			t.Errorf("looksHostLike(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWkQuietKeepsWarnings(t *testing.T) {
	f := apitest.New(t)
	r := wk(t, f, "-q", "--title", "x", "--test", "https://example.com", filepath.Join(t.TempDir(), "o.pdf"))
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "warning") || strings.Contains(r.stderr, "queued") || strings.Contains(r.stderr, "saved") {
		t.Fatalf("-q must drop progress lines but keep warnings:\n%s", r.stderr)
	}
}

func TestWkOutputHandling(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := wk(t, f, "--test", "https://example.com", filepath.Join(dir, "new", "deeper", "x.pdf"))
	if r.code != exitOK {
		t.Fatalf("missing directory must be created: %d %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "new", "deeper", "x.pdf")); err != nil {
		t.Fatal(err)
	}
	r = wk(t, f, "--test", "https://example.com", dir)
	if r.code != exitOK {
		t.Fatalf("existing directory as <output>: %d %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "job-1.pdf")); err != nil {
		t.Fatal("expected <job-id>.pdf inside the directory")
	}
	afile := filepath.Join(dir, "afile")
	os.WriteFile(afile, []byte("x"), 0o644)
	r = wk(t, f, "--test", "https://example.com", filepath.Join(afile, "x.pdf"))
	if r.code != exitUsage || !strings.Contains(r.stderr, "is a file") || strings.Contains(r.stderr, "queued") {
		t.Fatalf("parent-is-a-file must be refused before submit: %d %s", r.code, r.stderr)
	}
	r = wk(t, f, "--test", "https://example.com", "-")
	if r.code != exitOK || r.stdout != apitest.PDF {
		t.Fatalf("'-' must stream to stdout: %d %q", r.code, r.stdout)
	}
	if r := wk(t, nil, "https://example.com"); r.code != exitUsage || !strings.Contains(r.stderr, "<input> <output>") {
		t.Fatalf("missing output: %d %s", r.code, r.stderr)
	}
}

func TestWkUnsetHeaderIsBlankNotChromiumDefault(t *testing.T) {
	var body struct {
		Options struct {
			Header string `json:"header_template"`
			Footer string `json:"footer_template"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(buildBody(t, "--footer-right", "Printed [date]")), &body); err != nil {
		t.Fatal(err)
	}
	if body.Options.Header != "<span></span>" {
		t.Fatalf("header must be blanked explicitly, got %q", body.Options.Header)
	}
	if err := json.Unmarshal([]byte(buildBody(t, "--header-left", "x")), &body); err != nil {
		t.Fatal(err)
	}
	if body.Options.Footer != "<span></span>" {
		t.Fatalf("footer must be blanked explicitly, got %q", body.Options.Footer)
	}
}

func TestWkFlagTableInvariants(t *testing.T) {
	for name, f := range wkFlags {
		if !strings.HasPrefix(name, "-") && name != "toc" && name != "cover" {
			t.Errorf("%s: flag names start with '-' (object keywords excepted)", name)
		}
		switch f.class {
		case wkMapped:
			if f.apply == nil || f.reason != "" {
				t.Errorf("%s: a mapped flag needs apply and no reason", name)
			}
		default:
			if f.apply != nil || f.reason == "" {
				t.Errorf("%s: an %s flag needs a reason and no apply", name, wkClassName(f.class))
			}
		}
		if f.values < 0 || f.values > 2 {
			t.Errorf("%s: values out of range", name)
		}
	}
	for _, pair := range [][2]string{{"-s", "--page-size"}, {"-T", "--margin-top"}, {"-q", "--quiet"}, {"-g", "--grayscale"}} {
		if wkFlags[pair[0]] != wkFlags[pair[1]] {
			t.Errorf("%s and %s must share one definition", pair[0], pair[1])
		}
	}
}

// TestWkCompatibilityDocMatchesTable keeps COMPATIBILITY.md honest: every flag in
// the table appears under the right heading, and nothing is documented that the
// table does not implement.
func TestWkCompatibilityDocMatchesTable(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "COMPATIBILITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]wkFlagClass{
		"## Mapped flags":            wkMapped,
		"## Accepted with a warning": wkIgnorable,
		"## Refused":                 wkUnsupported,
	}
	token := regexp.MustCompile("`([^`]+)`")
	documented := map[wkFlagClass]map[string]bool{wkMapped: {}, wkIgnorable: {}, wkUnsupported: {}}
	current, inSection := wkFlagClass(-1), false
	for _, line := range strings.Split(string(doc), "\n") {
		if strings.HasPrefix(line, "## ") {
			c, ok := sections[strings.TrimSpace(line)]
			current, inSection = c, ok
			continue
		}
		if !inSection || !strings.HasPrefix(line, "| `") {
			continue
		}
		firstCell := strings.SplitN(line[1:], "|", 2)[0]
		for _, m := range token.FindAllStringSubmatch(firstCell, -1) {
			documented[current][m[1]] = true
		}
	}
	for class, want := range map[wkFlagClass][]string{wkMapped: wkFlagNames(wkMapped), wkIgnorable: wkFlagNames(wkIgnorable), wkUnsupported: wkFlagNames(wkUnsupported)} {
		for _, name := range want {
			if !documented[class][name] {
				t.Errorf("%s flag %s is not documented in COMPATIBILITY.md under its section", wkClassName(class), name)
			}
		}
		for name := range documented[class] {
			if f, ok := wkFlags[name]; !ok || f.class != class {
				t.Errorf("COMPATIBILITY.md documents %s as %s, but the table disagrees", name, wkClassName(class))
			}
		}
	}
}

func TestWkTwoValuedFlagsConsumeBothArguments(t *testing.T) {
	st := newWkState()
	if err := parseWkArgs(st, []string{"--replace", "name", "value", "in.html", "out.pdf"}); err != nil {
		t.Fatal(err)
	}
	if st.replacements["name"] != "value" || len(st.positional) != 2 {
		t.Fatalf("replace parsing wrong: %v %v", st.replacements, st.positional)
	}
}

func TestWkStdinInput(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, strings.NewReader("<h1>stdin</h1>"), "wkhtmltopdf", "-", filepath.Join(dir, "stdin.pdf"))
	if r.code != exitOK || f.LastBody(t)["html"] != "<h1>stdin</h1>" {
		t.Fatalf("stdin input: exit %d body=%v: %s", r.code, f.LastBody(t)["html"], r.stderr)
	}
	mustContain(t, r.stderr, "sanitizes HTML input")

	r = execute(t, f, strings.NewReader("<h1>x</h1>"), "wkhtmltopdf", "--username", "bob", "-", filepath.Join(dir, "auth.pdf"))
	if r.code != exitUsage || !strings.Contains(r.stderr, "URL input") || f.Submits() != 1 {
		t.Fatalf("auth with stdin input must be refused before submit: exit %d submits=%d: %s", r.code, f.Submits(), r.stderr)
	}
}

func TestWkWebpageForLocalInput(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "in.html")
	os.WriteFile(file, []byte("<h1>x</h1>"), 0o644)
	r := wk(t, f, "--default-header", file, filepath.Join(dir, "out.pdf"))
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	opts := f.LastBody(t)["options"].(map[string]any)
	header, _ := opts["header_template"].(string)
	if strings.Contains(header, `class="url"`) || !strings.Contains(header, "file://") || !strings.Contains(header, "in.html") {
		t.Fatalf("[webpage] for a local file must be the file:// path, got %s", header)
	}
}

func TestWkHeaderHTMLAcceptsFileURL(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "my footer.html")
	os.WriteFile(file, []byte(`<p>[page]</p>`), 0o644)
	st := newWkState()
	if err := parseWkArgs(st, []string{"--footer-html", "file://" + filepath.ToSlash(strings.ReplaceAll(file, " ", "%20"))}); err != nil {
		t.Fatal(err)
	}
	body, err := st.buildSubmission()
	if err != nil {
		t.Fatalf("file:// template must be read as a local file: %v", err)
	}
	if tpl, _ := body["options"].(map[string]any)["footer_template"].(string); !strings.Contains(tpl, `class="pageNumber"`) {
		t.Fatalf("template not substituted: %s", tpl)
	}
}

func TestWkValueFlagsAndLoadErrorHandling(t *testing.T) {
	st := newWkState()
	if err := parseWkArgs(st, []string{"--bypass-proxy-for", "intranet.example", "--load-error-handling", "ignore", "in.html", "out.pdf"}); err != nil {
		t.Fatal(err)
	}
	if len(st.positional) != 2 {
		t.Fatalf("--bypass-proxy-for must consume its value: positionals=%v", st.positional)
	}
	if len(st.warnings) != 2 {
		t.Fatalf("expected two warnings (bypass + ignore note), got %v", st.warnings)
	}
	for _, args := range [][]string{{"--load-error-handling", "abort"}, {"--load-error-handling", "bogus"}, {"--replace", "bad name", "x"}} {
		if err := parseWkArgs(newWkState(), args); err == nil {
			t.Fatalf("%v: expected a refusal", args)
		}
	}
	if err := parseWkArgs(newWkState(), []string{"--load-media-error-handling", "skip"}); err != nil {
		t.Fatalf("skip must be accepted: %v", err)
	}
}

func TestWkSitePageTokensMapToPageNumbers(t *testing.T) {
	got := buildBody(t, "--footer-center", "[sitepage]/[sitepages]")
	if !strings.Contains(got, `class=\"pageNumber\"`) || !strings.Contains(got, `class=\"totalPages\"`) {
		t.Fatalf("sitepage tokens must map to page numbers: %s", got)
	}
}
