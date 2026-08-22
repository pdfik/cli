package main

// The wkhtmltopdf compatibility mode: `pdfik wkhtmltopdf [options] <input> <output>`.
//
// This is a translator, not an emulator — it maps the common wkhtmltopdf flags
// onto the same API client the rest of the CLI uses (see wkflags.go for the
// three flag classes and COMPATIBILITY.md for the user-facing list). Fidelity
// defaults match wkhtmltopdf: backgrounds print unless --no-background, margins
// default to 10mm, [page]-style placeholders work in headers and footers.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdfik/cli/internal/api"
)

type wkState struct {
	options      map[string]any
	margin       map[string]string
	render       map[string]any
	header       headerFooter
	footer       headerFooter
	replacements map[string]string // --replace name value, lower-cased names
	docTitle     string            // --title, for [title]/[doctitle]
	inputURL     string            // the classified <input>, for [webpage]
	inputIsURL   bool
	authUser     *string
	authPass     *string
	quiet        bool
	test         bool
	conn         connFlags
	timeout      time.Duration
	positional   []string
	warnings     []string
}

type headerFooter struct {
	left, center, right string
	fontSizePt          string
	fontName            string
	line                *bool
	htmlFile            string
	used                bool
}

func newWkState() *wkState {
	return &wkState{
		options:      map[string]any{},
		margin:       map[string]string{},
		render:       map[string]any{},
		replacements: map[string]string{},
		timeout:      defaultTimeout,
	}
}

func (st *wkState) warn(msg string) { st.warnings = append(st.warnings, msg) }

// Sentinels for the two wkhtmltopdf probes wrappers send before the first
// render (`wkhtmltopdf --version`, `-h`); they are handled anywhere in argv.
var (
	errWkHelp    = errors.New("help requested")
	errWkVersion = errors.New("version requested")
)

const wkModeUsage = `pdfik wkhtmltopdf [wkhtmltopdf-flags] <input> <output>

Drop-in for the common wkhtmltopdf invocations: <input> is a URL (https://…) or
a local HTML file ('-' for stdin); <output> is the PDF path ('-' for stdout; an
existing directory names the file <job-id>.pdf). The directory is created if
missing — checked before anything is submitted.

Mapped flags (same meaning as in wkhtmltopdf):
  -s/--page-size  -O/--orientation  -T/-B/-L/-R and --margin-*  --background
  --no-background  --zoom  --disable-smart-shrinking  --header-*/--footer-*
  (left/center/right text with [page] [topage] [date] [title] [webpage],
  font-size, font-name, line, html)  --default-header  --replace  --title
  --javascript-delay  --username/--password  --image-quality  --encoding  -q
Flags with no effect here are accepted with a warning; flags that would change
the output are refused with a reason — the full lists are in COMPATIBILITY.md.

Extensions:
  --test  --api-key KEY  --api-url URL  --timeout DUR   (see 'pdfik help')
`

// cmdWkhtmltopdf implements the compatibility mode on top of renderAndSave.
func cmdWkhtmltopdf(ctx context.Context, args []string, std streams) error {
	st := newWkState()
	err := parseWkArgs(st, args)
	switch {
	case errors.Is(err, errWkHelp):
		fmt.Fprint(std.out, wkModeUsage)
		return nil
	case errors.Is(err, errWkVersion):
		fmt.Fprintln(std.out, "pdfik "+effectiveVersion()+" (wkhtmltopdf compatibility mode)")
		return nil
	case err != nil:
		return err
	}
	if len(st.positional) != 2 {
		return usagef("expected <input> <output> (one input document — see COMPATIBILITY.md)")
	}
	input, output := st.positional[0], st.positional[1]

	// Settle the destination BEFORE anything is submitted: a bad path must never
	// cost a render.
	if output == stdoutMarker && stdoutIsTerminal(std.out) {
		return usagef("refusing to write PDF bytes to a terminal — redirect stdout or use a file path")
	}
	if err := prepareWkOutput(output); err != nil {
		return err
	}

	input, isURL := classifyWkInput(input)
	st.inputURL, st.inputIsURL = input, isURL
	if !isURL {
		if input != "-" {
			if _, statErr := os.Stat(input); statErr != nil {
				msg := fmt.Sprintf("%s: %v", input, underlying(statErr))
				if looksHostLike(input) {
					// wkhtmltopdf auto-prepended http:// to schemeless input; an
					// explicit scheme is required here.
					msg += fmt.Sprintf(" (for a URL, add the scheme: https://%s)", input)
				}
				return usagef("%s", msg)
			}
		}
		if st.authUser != nil || st.authPass != nil {
			return usagef("--username/--password only apply to URL input")
		}
		st.warn("local HTML input: relative images/CSS are not resolved, and the API sanitizes HTML input (styles/scripts stripped) — URL input renders with full fidelity; see COMPATIBILITY.md")
	}

	body, err := st.buildSubmission()
	// Warnings are the honesty contract of this mode and are never silenced:
	// -q drops progress chatter, not diagnostics.
	for _, w := range st.warnings {
		fmt.Fprintln(std.err, "pdfik wkhtmltopdf: warning:", w)
	}
	if err != nil {
		return err
	}

	var submit func(context.Context, *api.Client) (api.Job, error)
	if isURL {
		if st.authUser != nil || st.authPass != nil {
			// wkhtmltopdf sends basic auth whenever either half is present, the
			// other defaulting to empty — token-as-username schemes rely on it.
			user, pass := "", ""
			if st.authUser != nil {
				user = *st.authUser
			}
			if st.authPass != nil {
				pass = *st.authPass
			}
			body["auth"] = map[string]string{"type": "basic", "value": user + ":" + pass}
		}
		submit = func(ctx context.Context, c *api.Client) (api.Job, error) { return c.SubmitURL(ctx, input, body) }
	} else {
		html, err := readInput(input, std.in)
		if err != nil {
			return err
		}
		submit = func(ctx context.Context, c *api.Client) (api.Job, error) { return c.SubmitHTML(ctx, html, body) }
	}

	client, err := st.conn.newClient(std.err)
	if err != nil {
		return err
	}
	return renderAndSave(ctx, client, submit, func(string) string { return output },
		runOptions{timeout: st.timeout, quiet: st.quiet, test: st.test}, std)
}

// classifyWkInput recognises URL input. file:// is a local path in wkhtmltopdf
// and stays one here (percent-decoded, as Qt decoded it).
func classifyWkInput(input string) (string, bool) {
	if rest, ok := strings.CutPrefix(input, "file://"); ok {
		if decoded, err := url.PathUnescape(rest); err == nil {
			rest = decoded
		}
		// file:///C:/x carries a Windows drive behind the slash; file:///tmp/x is
		// an absolute POSIX path and keeps it.
		if len(rest) >= 3 && rest[0] == '/' && rest[2] == ':' {
			rest = rest[1:]
		}
		return filepath.FromSlash(rest), false
	}
	return input, strings.Contains(input, "://")
}

// looksHostLike guesses whether a non-existent "file" argument was meant as a
// schemeless URL: the first path segment carries a dot or a :port, is not a
// Windows drive path, not a dot-segment and not a document file name.
func looksHostLike(s string) bool {
	if len(s) >= 3 && s[1] == ':' && (s[2] == '/' || s[2] == '\\') {
		return false // C:\path or C:/path
	}
	first := s
	if i := strings.IndexAny(s, `/\`); i >= 0 {
		first = s[:i]
	}
	if first == "." || first == ".." {
		return false
	}
	switch strings.ToLower(filepath.Ext(first)) {
	case ".html", ".htm", ".xhtml":
		return false
	}
	return strings.Contains(first, ".") || strings.Contains(first, ":")
}

// prepareWkOutput makes sure the positional <output> can be written to. "-"
// streams to stdout; an existing directory is accepted as is (the file is
// named after the job id inside it); otherwise the parent directory is created
// if missing, and a parent that is a plain file is refused.
func prepareWkOutput(output string) error {
	if output == stdoutMarker {
		return nil
	}
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		return nil
	}
	return prepareOutputDir(filepath.Dir(output), "the <output> directory")
}

// webpageText is what [webpage] prints for non-URL input: wkhtmltopdf showed
// the file:// form of the document; Chromium's url class would print
// about:blank for HTML that was submitted as text.
func (st *wkState) webpageText() string {
	if st.inputURL == "-" {
		return "stdin"
	}
	abs, err := filepath.Abs(st.inputURL)
	if err != nil {
		abs = st.inputURL
	}
	return "file://" + filepath.ToSlash(abs)
}

// parseWkArgs walks argv in wkhtmltopdf's own style: flags and positionals in
// any order, `--flag value` and `--flag=value` both accepted.
func parseWkArgs(st *wkState, args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, inlineValue, hasInline := strings.Cut(arg, "=")

		// Probes and extensions first — they are not wkhtmltopdf flags.
		switch name {
		case "-h", "--help", "-H", "--extended-help":
			return errWkHelp
		case "-V", "--version":
			return errWkVersion
		case "--test":
			st.test = true
			continue
		case "--api-key", "--api-url", "--timeout":
			value := inlineValue
			if !hasInline {
				if i+1 >= len(args) {
					return usagef("%s needs a value", name)
				}
				i++
				value = args[i]
			}
			switch name {
			case "--api-key":
				st.conn.apiKey = value
			case "--api-url":
				st.conn.apiURL = value
			case "--timeout":
				d, err := parseTimeout(value)
				if err != nil {
					return usagef("--timeout: %v", err)
				}
				st.timeout = d
			}
			continue
		}

		spec, known := wkFlags[name]
		if !known {
			if strings.HasPrefix(name, "-") && name != "-" {
				return usagef("unknown flag %s — see COMPATIBILITY.md at https://github.com/pdfik/cli", name)
			}
			st.positional = append(st.positional, arg)
			continue
		}

		var values []string
		if spec.values > 0 {
			if hasInline {
				values = append(values, inlineValue)
			}
			for len(values) < spec.values {
				if i+1 >= len(args) {
					return usagef("%s needs %d value(s)", name, spec.values)
				}
				i++
				values = append(values, args[i])
			}
		}

		switch spec.class {
		case wkUnsupported:
			return usagef("%s is not supported: %s (see COMPATIBILITY.md)", name, spec.reason)
		case wkIgnorable:
			st.warn(name + " ignored: " + spec.reason)
		case wkMapped:
			if err := spec.apply(st, values); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildSubmission turns the parsed state into the API request body.
func (st *wkState) buildSubmission() (api.Submission, error) {
	// wkhtmltopdf fidelity defaults.
	if _, set := st.options["print_background"]; !set {
		st.options["print_background"] = true
	}
	for _, side := range marginSides {
		if _, set := st.margin[side]; !set {
			st.margin[side] = "10mm"
		}
	}
	st.options["margin"] = st.margin

	for _, part := range []struct {
		hf     *headerFooter
		key    string
		footer bool
	}{{&st.header, "header_template", false}, {&st.footer, "footer_template", true}} {
		tpl, err := part.hf.template(part.footer, st)
		if err != nil {
			return nil, err
		}
		if tpl != "" {
			st.options[part.key] = tpl
			st.options["display_header_footer"] = true
		}
	}
	// Chromium treats an EMPTY template as "use the default" (date + title on
	// top, URL + page numbers below); wkhtmltopdf printed nothing for an unset
	// part. Send a blank template for whichever part the user did not set.
	if st.options["display_header_footer"] == true {
		for _, key := range []string{"header_template", "footer_template"} {
			if _, set := st.options[key]; !set {
				st.options[key] = "<span></span>"
			}
		}
	}

	body := api.Submission{"options": st.options}
	if len(st.render) > 0 {
		body["render"] = st.render
	}
	if st.test {
		body["test"] = true
	}
	return body, nil
}
