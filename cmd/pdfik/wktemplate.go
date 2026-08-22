package main

import (
	"html"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// wkTokenPattern matches a bracketed placeholder: wkhtmltopdf's own
// ([page], [topage], …) and the names given to --replace. wkhtmltopdf
// substitutes case-insensitively — its own documentation spells "[toPage]" —
// so the match is case-insensitive too.
var wkTokenPattern = regexp.MustCompile(`(?i)\[([a-z][a-z0-9_-]*)\]`)

// wkLiteralTokens are wkhtmltopdf placeholders with no equivalent here; they
// print literally, with a warning.
var wkLiteralTokens = map[string]bool{"section": true, "subsection": true, "subsubsection": true}

// wkTokenClass maps the placeholders Chromium can fill in at print time onto
// its substitution classes. The rest are resolved at submission time or, when
// there is no sensible equivalent, left literal with a warning.
var wkTokenClass = map[string]string{
	"page":      "pageNumber",
	"topage":    "totalPages",
	"sitepage":  "pageNumber", // one document per run, so site == document
	"sitepages": "totalPages",
	"date":      "date",
	"title":     "title",
	"doctitle":  "title",
	"webpage":   "url",
}

// wkReplaceNamePattern is the shape a --replace name must have to be matched
// by wkTokenPattern at all.
var wkReplaceNamePattern = regexp.MustCompile(`(?i)^[a-z][a-z0-9_-]*$`)

// Injectable for deterministic tests.
var wkNow = time.Now

// substituteTokens resolves --replace names and wkhtmltopdf placeholders in
// s. With escape, s is user text and is HTML-escaped first (the escape runs
// before substitution, so user text can never inject markup into the
// template); replacement values are always escaped.
func (st *wkState) substituteTokens(s string, escape bool) string {
	if escape {
		s = html.EscapeString(s)
	}
	return wkTokenPattern.ReplaceAllStringFunc(s, func(token string) string {
		name := strings.ToLower(token[1 : len(token)-1])
		if v, ok := st.replacements[name]; ok {
			return html.EscapeString(v)
		}
		if (name == "title" || name == "doctitle") && st.docTitle != "" {
			return html.EscapeString(st.docTitle)
		}
		if name == "webpage" && !st.inputIsURL {
			return html.EscapeString(st.webpageText())
		}
		if class, ok := wkTokenClass[name]; ok {
			return `<span class="` + class + `"></span>`
		}
		now := wkNow()
		switch name {
		case "frompage":
			return "1"
		case "isodate":
			st.warnOnce(token + " is resolved at submission time")
			return now.Format("2006-01-02")
		case "time":
			st.warnOnce(token + " is resolved at submission time")
			return now.Format("15:04:05")
		}
		if wkLiteralTokens[name] {
			st.warnOnce(token + " has no equivalent here and will print literally")
		}
		return token // not a placeholder — ordinary bracketed text
	})
}

func (st *wkState) warnOnce(msg string) {
	for _, w := range st.warnings {
		if w == msg {
			return
		}
	}
	st.warn(msg)
}

// wkJSContract spots the classic wkhtmltopdf header file, which receives
// [page]/[topage] as query parameters and fills them in with JavaScript.
// Chromium templates never execute scripts, so that contract would render
// blank page numbers — refusing beats silently wrong output.
var wkJSContract = regexp.MustCompile(`(?i)<script|location\.search|\bsubst\s*\(`)

// templateCharLimit is the API's cap on a header/footer template, in
// CHARACTERS (not bytes).
const templateCharLimit = 10000

// template renders the wkhtmltopdf three-slot header/footer as the HTML
// template the API expects. The layout uses only CSS properties from the
// server sanitizer's allowlist (display, width, text-align, font-*, border-*,
// padding) — flexbox properties would be stripped and the slots would
// collapse.
func (hf *headerFooter) template(isFooter bool, st *wkState) (string, error) {
	if hf.htmlFile != "" {
		return hf.templateFromFile(st)
	}
	if !hf.used {
		return "", nil
	}
	size := hf.fontSizePt
	if size == "" {
		size = "12"
	}
	font := "Arial, sans-serif" // wkhtmltopdf's default header font
	if hf.fontName != "" {
		font = "'" + hf.fontName + "', sans-serif" // quoted: family names may contain spaces
	}
	border := ""
	if hf.line != nil && *hf.line {
		if isFooter {
			border = "border-top:1px solid #000;"
		} else {
			border = "border-bottom:1px solid #000;"
		}
	}
	// Align the slots with the body: pad by the page's own side margins.
	padding := "padding:0 " + st.margin["right"] + " 0 " + st.margin["left"] + ";"
	slot := func(txt, align string) string {
		return `<span style="display:inline-block;width:33%;text-align:` + align + `;vertical-align:top">` +
			st.substituteTokens(txt, true) + `</span>`
	}
	return `<div style="display:block;width:100%;font-size:` + size + `pt;font-family:` + font + `;` + padding + border + `">` +
		slot(hf.left, "left") + slot(hf.center, "center") + slot(hf.right, "right") + `</div>`, nil
}

func (hf *headerFooter) templateFromFile(st *wkState) (string, error) {
	path, isURL := classifyWkInput(hf.htmlFile) // file:// is a local file here too
	if isURL {
		return "", usagef("--header-html/--footer-html with a URL is not supported; pass a local file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", usagef("--header-html/--footer-html: %v", err)
	}
	content := string(b)
	if wkJSContract.MatchString(content) {
		return "", usagef("%s uses wkhtmltopdf's JavaScript substitution contract, which cannot work here — use [page]/[topage] tokens in a static fragment instead", hf.htmlFile)
	}
	st.warn(hf.htmlFile + ": the API keeps only basic markup in templates (span/div/p/b/i/strong/em/small/br + safe styles); img, tables and links are stripped — see COMPATIBILITY.md")
	// Bracket tokens in template files are a pdfik extension (wkhtmltopdf used
	// query params + JS there); harmless for files that contain none.
	content = st.substituteTokens(content, false)
	if n := utf8.RuneCountInString(content); n > templateCharLimit {
		return "", usagef("%s is %d characters after placeholder expansion; the API caps templates at %d", hf.htmlFile, n, templateCharLimit)
	}
	return content, nil
}
