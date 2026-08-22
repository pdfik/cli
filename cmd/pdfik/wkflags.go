package main

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The wkhtmltopdf flag table.
//
// Every wkhtmltopdf flag a migrating script may contain falls into one of
// three honest classes:
//
//   - mapped:      translated to an API option with the same meaning;
//   - ignorable:   no effect on the output this pipeline produces — accepted
//                  with a warning on stderr;
//   - unsupported: WOULD change the output — refused with a one-line reason,
//                  which beats silently rendering something different.
//
// COMPATIBILITY.md lists all three classes; a test keeps it in sync with this
// table, so a flag can never be added here without being documented.

type wkFlagClass int

const (
	wkMapped wkFlagClass = iota
	wkIgnorable
	wkUnsupported
)

type wkFlag struct {
	class  wkFlagClass
	values int // how many arguments the flag consumes (0, 1 or 2)
	apply  func(st *wkState, values []string) error
	reason string // shown for ignorable (warning) and unsupported (error) flags
}

// wkFlagDef declares one flag under all of its spellings; aliases share one
// *wkFlag so short and long forms cannot drift apart.
type wkFlagDef struct {
	names []string
	flag  wkFlag
}

var wkFlags = buildWkFlags(wkFlagDefs())

func buildWkFlags(defs []wkFlagDef) map[string]*wkFlag {
	table := make(map[string]*wkFlag, len(defs))
	for i := range defs {
		f := &defs[i].flag
		for _, name := range defs[i].names {
			table[name] = f
		}
	}
	return table
}

// wkFlagNames lists the flags of one class, sorted — used by the
// documentation-parity test.
func wkFlagNames(class wkFlagClass) []string {
	var names []string
	for name, f := range wkFlags {
		if f.class == class {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func mapped(values int, apply func(st *wkState, values []string) error) wkFlag {
	return wkFlag{class: wkMapped, values: values, apply: apply}
}

func ignorable(values int, reason string) wkFlag {
	return wkFlag{class: wkIgnorable, values: values, reason: reason}
}

func unsupported(values int, reason string) wkFlag {
	return wkFlag{class: wkUnsupported, values: values, reason: reason}
}

// setOption is a mapped flag that stores a fixed option value.
func setOption(key string, value any) wkFlag {
	return mapped(0, func(st *wkState, _ []string) error { st.options[key] = value; return nil })
}

// marginFlag maps -T/-B/-L/-R. Bare numbers are millimetres, as in
// wkhtmltopdf; pt — the one wkhtmltopdf unit the API lacks — is converted.
func marginFlag(side, flagName string) wkFlag {
	return mapped(1, func(st *wkState, v []string) error {
		value, err := normalizeWkLength(flagName, v[0])
		if err != nil {
			return err
		}
		st.margin[side] = value
		return nil
	})
}

func normalizeWkLength(flagName, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return "", usagef("%s %q is not a valid length", flagName, raw)
		}
		v = strconv.FormatFloat(f, 'f', -1, 64) + "mm"
	} else if n, ok := strings.CutSuffix(v, "pt"); ok {
		f, err := strconv.ParseFloat(n, 64)
		if err != nil || f < 0 || math.IsInf(f, 0) {
			return "", usagef("%s %q is not a valid length", flagName, raw)
		}
		v = strconv.FormatFloat(f*25.4/72, 'f', 3, 64) + "mm"
	}
	if err := checkLength(flagName, v); err != nil {
		return "", err
	}
	return v, nil
}

func headerFooterText(part func(*wkState) *headerFooter, slot string) wkFlag {
	return mapped(1, func(st *wkState, v []string) error {
		hf := part(st)
		hf.used = true
		switch slot {
		case "left":
			hf.left = v[0]
		case "center":
			hf.center = v[0]
		case "right":
			hf.right = v[0]
		}
		return nil
	})
}

func headerFooterLine(part func(*wkState) *headerFooter, on bool) wkFlag {
	return mapped(0, func(st *wkState, _ []string) error {
		hf := part(st)
		hf.line = &on
		if on {
			hf.used = true
		}
		return nil
	})
}

// fontNamePattern admits family names such as "Noto Sans CJK JP" or
// "Segoe UI"; the value is quoted when emitted, so quotes themselves are out.
var fontNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 \-]*$`)

func headerFooterFontSize(part func(*wkState) *headerFooter, flagName string) wkFlag {
	return mapped(1, func(st *wkState, v []string) error {
		f, err := strconv.ParseFloat(v[0], 64)
		if err != nil || f <= 0 || f > 200 {
			return usagef("%s %q is not a valid size in points", flagName, v[0])
		}
		part(st).fontSizePt = strconv.FormatFloat(f, 'f', -1, 64)
		return nil
	})
}

func headerFooterFontName(part func(*wkState) *headerFooter, flagName string) wkFlag {
	return mapped(1, func(st *wkState, v []string) error {
		if !fontNamePattern.MatchString(v[0]) {
			return usagef("%s %q is not a valid font name", flagName, v[0])
		}
		part(st).fontName = v[0]
		return nil
	})
}

func headerFooterFile(part func(*wkState) *headerFooter) wkFlag {
	return mapped(1, func(st *wkState, v []string) error {
		hf := part(st)
		hf.htmlFile = v[0]
		hf.used = true
		return nil
	})
}

func header(st *wkState) *headerFooter { return &st.header }
func footer(st *wkState) *headerFooter { return &st.footer }

func wkFlagDefs() []wkFlagDef {
	return []wkFlagDef{
		// ---- mapped ------------------------------------------------------
		{[]string{"-s", "--page-size"}, mapped(1, func(st *wkState, v []string) error {
			if err := validateFormat(v[0]); err != nil {
				return err
			}
			st.options["format"] = v[0]
			return nil
		})},
		{[]string{"-O", "--orientation"}, mapped(1, func(st *wkState, v []string) error {
			switch {
			case strings.EqualFold(v[0], "Landscape"):
				st.options["landscape"] = true
			case strings.EqualFold(v[0], "Portrait"):
				st.options["landscape"] = false
			default:
				return usagef("--orientation %q is not valid (Portrait or Landscape)", v[0])
			}
			return nil
		})},
		{[]string{"-T", "--margin-top"}, marginFlag("top", "--margin-top")},
		{[]string{"-B", "--margin-bottom"}, marginFlag("bottom", "--margin-bottom")},
		{[]string{"-L", "--margin-left"}, marginFlag("left", "--margin-left")},
		{[]string{"-R", "--margin-right"}, marginFlag("right", "--margin-right")},
		{[]string{"--background"}, setOption("print_background", true)},
		{[]string{"--no-background"}, setOption("print_background", false)},
		{[]string{"-q", "--quiet"}, mapped(0, func(st *wkState, _ []string) error { st.quiet = true; return nil })},
		{[]string{"--zoom"}, mapped(1, func(st *wkState, v []string) error {
			f, err := strconv.ParseFloat(v[0], 64)
			if err != nil || math.IsNaN(f) || f < 0.5 || f > 2.0 {
				return usagef("--zoom %q is outside the supported 0.5–2.0 range", v[0])
			}
			st.options["scale"] = f
			return nil
		})},
		// The renderer auto-fits content wider than the printable area whenever
		// scale is omitted — functionally wkhtmltopdf's smart shrinking, so
		// disabling it maps exactly to a fixed scale of 1.
		{[]string{"--disable-smart-shrinking"}, mapped(0, func(st *wkState, _ []string) error {
			if _, set := st.options["scale"]; !set {
				st.options["scale"] = 1.0
			}
			return nil
		})},
		{[]string{"--javascript-delay"}, mapped(1, func(st *wkState, v []string) error {
			ms, err := strconv.Atoi(v[0])
			if err != nil || ms < 0 || ms > 10000 {
				return usagef("--javascript-delay %q is outside the 0–10000 ms range", v[0])
			}
			st.render["wait_after_load_ms"] = ms // render options are Pro+ (see COMPATIBILITY.md)
			return nil
		})},
		{[]string{"--username"}, mapped(1, func(st *wkState, v []string) error { st.authUser = &v[0]; return nil })},
		{[]string{"--password"}, mapped(1, func(st *wkState, v []string) error { st.authPass = &v[0]; return nil })},
		{[]string{"--image-quality"}, mapped(1, func(st *wkState, v []string) error {
			q, err := strconv.Atoi(v[0])
			if err != nil || q < 1 || q > 100 {
				return usagef("--image-quality %q is outside the 1–100 range", v[0])
			}
			st.options["compression"] = map[string]any{"image_quality": q} // Pro+ (see COMPATIBILITY.md)
			return nil
		})},
		{[]string{"--header-left"}, headerFooterText(header, "left")},
		{[]string{"--header-center"}, headerFooterText(header, "center")},
		{[]string{"--header-right"}, headerFooterText(header, "right")},
		{[]string{"--footer-left"}, headerFooterText(footer, "left")},
		{[]string{"--footer-center"}, headerFooterText(footer, "center")},
		{[]string{"--footer-right"}, headerFooterText(footer, "right")},
		{[]string{"--header-font-size"}, headerFooterFontSize(header, "--header-font-size")},
		{[]string{"--footer-font-size"}, headerFooterFontSize(footer, "--footer-font-size")},
		{[]string{"--header-font-name"}, headerFooterFontName(header, "--header-font-name")},
		{[]string{"--footer-font-name"}, headerFooterFontName(footer, "--footer-font-name")},
		{[]string{"--header-line"}, headerFooterLine(header, true)},
		{[]string{"--footer-line"}, headerFooterLine(footer, true)},
		{[]string{"--no-header-line"}, headerFooterLine(header, false)},
		{[]string{"--no-footer-line"}, headerFooterLine(footer, false)},
		{[]string{"--header-html"}, headerFooterFile(header)},
		{[]string{"--footer-html"}, headerFooterFile(footer)},
		// --default-header is wkhtmltopdf's shorthand for a webpage/page-count
		// header with a rule under it.
		{[]string{"--default-header"}, mapped(0, func(st *wkState, _ []string) error {
			st.header.left, st.header.right, st.header.used = "[webpage]", "[page]/[topage]", true
			on := true
			st.header.line = &on
			return nil
		})},
		{[]string{"--replace"}, mapped(2, func(st *wkState, v []string) error {
			if !wkReplaceNamePattern.MatchString(v[0]) {
				return usagef("--replace: %q cannot be a placeholder name (letters, digits, '_' and '-', starting with a letter)", v[0])
			}
			st.replacements[strings.ToLower(v[0])] = v[1]
			return nil
		})},
		{[]string{"--title"}, mapped(1, func(st *wkState, v []string) error {
			st.docTitle = v[0]
			st.warn("--title applies to [title] in headers and footers; the PDF metadata title is not settable via the API")
			return nil
		})},
		{[]string{"--encoding"}, mapped(1, func(st *wkState, v []string) error {
			// UTF-8 spellings are a no-op note; anything else WOULD change the
			// output (mojibake), so it is refused per this mode's taxonomy.
			norm := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(v[0]))
			switch norm {
			case "utf8", "utf", "usascii", "ascii":
				st.warn("--encoding " + v[0] + " noted: input is treated as UTF-8")
				return nil
			}
			return usagef("--encoding %q is not supported: input is submitted as UTF-8 — re-encode the file first (e.g. iconv -f %s -t utf-8)", v[0], v[0])
		})},

		// ---- ignorable: no effect on the output this pipeline produces -------
		{[]string{"--print-media-type"}, ignorable(0, "this pipeline always renders with print media CSS")},
		{[]string{"--enable-javascript"}, ignorable(0, "JavaScript is enabled for URL input (HTML input is sanitized — see COMPATIBILITY.md)")},
		{[]string{"--enable-smart-shrinking"}, ignorable(0, "content is auto-fitted by default, matching wkhtmltopdf")},
		{[]string{"--stop-slow-scripts", "--no-stop-slow-scripts"}, ignorable(0, "the script watchdog is handled by the rendering timeout")},
		{[]string{"--enable-local-file-access", "--disable-local-file-access"}, ignorable(0, "the HTML file itself is read locally; its relative assets are NOT — see COMPATIBILITY.md")},
		{[]string{"-d", "--dpi"}, ignorable(1, "PDF output is vector; DPI does not apply")},
		{[]string{"--image-dpi"}, ignorable(1, "image DPI is controlled by the source page")},
		{[]string{"-l", "--lowquality"}, ignorable(0, "use --image-quality N for lossy compression (Pro+)")},
		{[]string{"--header-spacing", "--footer-spacing"}, ignorable(1, "the gap between header/footer and body cannot be adjusted; layout may differ slightly")},
		{[]string{"--copies"}, ignorable(1, "copies only matter when printing on paper")},
		{[]string{"--collate", "--no-collate"}, ignorable(0, "collation only matters when printing on paper")},
		{[]string{"--log-level"}, ignorable(1, "verbosity is controlled by --quiet")},
		{[]string{"--no-outline"}, ignorable(0, "PDF outlines are never generated, so this is already the behaviour")},
		{[]string{"--dump-outline"}, ignorable(1, "PDF outlines are never generated")},
		{[]string{"--disable-external-links", "--enable-external-links", "--disable-internal-links", "--enable-internal-links"},
			ignorable(0, "link annotations follow Chromium's print output")},
		{[]string{"--disable-forms"}, ignorable(0, "interactive PDF forms are never generated")},
		{[]string{"--images"}, ignorable(0, "images are loaded by default")},
		{[]string{"--keep-relative-links", "--resolve-relative-links"}, ignorable(0, "link annotations follow Chromium's print output")},
		{[]string{"--debug-javascript", "--no-debug-javascript"}, ignorable(0, "script diagnostics are not reported by the API")},
		{[]string{"--disable-plugins", "--enable-plugins"}, ignorable(0, "browser plugins do not exist in the renderer")},
		{[]string{"--exclude-from-outline", "--include-in-outline", "--disable-toc-back-links", "--enable-toc-back-links"},
			ignorable(0, "PDF outlines and tables of contents are never generated")},
		{[]string{"--no-pdf-compression"}, ignorable(0, "stream compression is decided by the renderer")},
		{[]string{"--use-xserver"}, ignorable(0, "rendering happens in the cloud")},
		{[]string{"--custom-header-propagation", "--no-custom-header-propagation"}, ignorable(0, "custom request headers are not supported, so propagation does not apply")},
		{[]string{"--proxy-hostname-lookup"}, ignorable(0, "the renderer fetches directly; proxies are not supported")},
		{[]string{"--bypass-proxy-for"}, ignorable(1, "the renderer fetches directly; proxies are not supported")},
		{[]string{"--allow"}, ignorable(1, "local file access rules do not apply; the HTML file itself is read locally")},
		{[]string{"--cache-dir"}, ignorable(1, "there is no local rendering cache")},
		{[]string{"--checkbox-svg", "--checkbox-checked-svg", "--radiobutton-svg", "--radiobutton-checked-svg"},
			ignorable(1, "form controls are rendered by Chromium's print output")},

		// ---- unsupported: would change the rendered output ------------------
		{[]string{"-g", "--grayscale"}, unsupported(0, "the API has no grayscale conversion")},
		{[]string{"-n", "--disable-javascript"}, unsupported(0, "rendering without JavaScript is not supported for URL input")},
		{[]string{"--no-print-media-type"}, unsupported(0, "screen-media rendering is not supported")},
		{[]string{"--window-status"}, unsupported(1, "waiting on window.status is not supported by the API")},
		{[]string{"--cookie"}, unsupported(2, "per-request cookies are not supported by the API")},
		{[]string{"--cookie-jar"}, unsupported(1, "cookies are not supported by the API")},
		{[]string{"--custom-header"}, unsupported(2, "custom request headers are not supported by the API")},
		{[]string{"--page-width", "--page-height"}, unsupported(1, "custom page dimensions are not supported; use --page-size")},
		{[]string{"--page-offset"}, unsupported(1, "page numbers always start at 1 in Chromium's templates")},
		{[]string{"--read-args-from-stdin"}, unsupported(0, "batch mode is not supported yet")},
		{[]string{"toc"}, unsupported(0, "table-of-contents objects are not supported")},
		{[]string{"cover"}, unsupported(1, "cover pages are not supported")},
		{[]string{"--outline"}, unsupported(0, "PDF outlines are not generated")},
		{[]string{"--outline-depth"}, unsupported(1, "PDF outlines are not generated")},
		{[]string{"--dump-default-toc-xsl", "--xsl-style-sheet"}, unsupported(0, "tables of contents are not supported")},
		{[]string{"--user-style-sheet"}, unsupported(1, "injecting a stylesheet is not supported by the API")},
		{[]string{"--run-script"}, unsupported(1, "injecting scripts is not supported by the API")},
		{[]string{"--viewport-size"}, unsupported(1, "a custom viewport is not supported by the API")},
		{[]string{"--minimum-font-size"}, unsupported(1, "minimum font size is not supported by the API")},
		{[]string{"-p", "--proxy"}, unsupported(1, "proxying the fetch is not supported by the API")},
		{[]string{"--post", "--post-file"}, unsupported(2, "POST navigation is not supported by the API")},
		// The pipeline's own behaviour is wkhtmltopdf's `ignore`: a subresource
		// that fails to load renders missing, while a main document that fails
		// still fails the job. `abort` cannot be honoured.
		{[]string{"--load-error-handling", "--load-media-error-handling"}, mapped(1, func(st *wkState, v []string) error {
			switch strings.ToLower(v[0]) {
			case "ignore", "skip":
				st.warn("--load-error-handling " + v[0] + " is already the behaviour here: failed subresources render missing; a main document that fails to load still fails the job")
				return nil
			case "abort":
				return usagef("--load-error-handling abort is not supported: subresource load errors cannot abort the render here (the main document failing still fails the job)")
			}
			return usagef("--load-error-handling %q is not valid (abort, ignore or skip)", v[0])
		})},
		{[]string{"--enable-forms"}, unsupported(0, "interactive PDF forms are not supported")},
		{[]string{"--no-images"}, unsupported(0, "suppressing images is not supported")},
		{[]string{"--ssl-crt-path", "--ssl-key-path", "--ssl-key-password"}, unsupported(1, "client TLS certificates are not supported by the API")},
		{[]string{"--readme", "--htmldoc", "--manpage", "--license"}, unsupported(0, "documentation lives in README.md, COMPATIBILITY.md and LICENSE")},
	}
}

// wkClassName is the human label of a class, used in messages and tests.
func wkClassName(c wkFlagClass) string {
	switch c {
	case wkMapped:
		return "mapped"
	case wkIgnorable:
		return "ignorable"
	default:
		return "unsupported"
	}
}
