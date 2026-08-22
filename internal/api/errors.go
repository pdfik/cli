package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Error is an RFC 7807 problem response from the API, kept structured so the
// caller can react to specific cases (429 back-off, 402 quota) instead of
// pattern-matching message strings.
type Error struct {
	Status     int    `json:"status"`
	Type       string `json:"type"`
	Title      string `json:"title"`
	Code       string `json:"error"`
	RetryAfter string `json:"-"`

	// Detail is a string in every documented error; FastAPI validation errors
	// (422) put a list of {loc, msg, type} here instead.
	Detail any `json:"detail"`
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("API error %d", e.Status)
	if e.Code != "" {
		msg += " (" + e.Code + ")"
	}
	if detail := e.detailText(); detail != "" {
		msg += ": " + detail
	}
	if e.Type != "" {
		msg += "\n  reference: " + e.Type
	}
	return msg
}

// detailText flattens Detail: a string as is; a validation list as
// "field: message; field: message"; the object the API uses for plan and
// payment problems as one sentence.
func (e *Error) detailText() string {
	switch d := e.Detail.(type) {
	case nil:
		return ""
	case string:
		return d
	case map[string]any:
		if text := objectDetailText(d); text != "" {
			return text
		}
	case []any:
		var parts []string
		for _, item := range d {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			var loc []string
			if l, ok := m["loc"].([]any); ok {
				for _, p := range l {
					if s, ok := p.(string); ok && s != "body" {
						loc = append(loc, s)
					}
				}
			}
			msg, _ := m["msg"].(string)
			msg = strings.TrimPrefix(msg, "Value error, ")
			switch {
			case len(loc) > 0 && msg != "":
				parts = append(parts, strings.Join(loc, ".")+": "+msg)
			case msg != "":
				parts = append(parts, msg)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	b, err := json.Marshal(e.Detail)
	if err != nil {
		return ""
	}
	return string(b)
}

// objectDetailText renders the object-shaped details the API sends with 402:
// {"error":"PAYMENT_METHOD_REQUIRED","message":…,"add_card_url":…} and
// {"error":"PLAN_UPGRADE_REQUIRED","feature":…,"requiredPlan":…,"upgrade_url":…}.
func objectDetailText(d map[string]any) string {
	str := func(key string) string { s, _ := d[key].(string); return s }
	var parts []string
	switch {
	case str("message") != "":
		parts = append(parts, str("message"))
	case str("feature") != "" && str("requiredPlan") != "":
		parts = append(parts, fmt.Sprintf("%s requires the %s plan or higher", str("feature"), str("requiredPlan")))
	case str("detail") != "":
		parts = append(parts, str("detail"))
	}
	for _, key := range []string{"upgrade_url", "add_card_url"} {
		if u := str(key); u != "" {
			parts = append(parts, u)
		}
	}
	return strings.Join(parts, " — ")
}

// retryAfter returns the server's Retry-After (seconds or an HTTP date),
// capped so a server-controlled header cannot park the client, or fallback.
func (e *Error) retryAfter(fallback time.Duration, now func() time.Time) time.Duration {
	d := fallback
	capSeconds := int64(retryAfterCap / time.Second)
	if s, err := strconv.ParseInt(strings.TrimSpace(e.RetryAfter), 10, 64); err == nil && s > 0 {
		if s >= capSeconds { // compare before multiplying: huge values would overflow
			return retryAfterCap
		}
		d = time.Duration(s) * time.Second
	} else if t, err := http.ParseTime(e.RetryAfter); err == nil {
		if until := t.Sub(now()); until > 0 {
			d = until
		}
	}
	if d > retryAfterCap {
		d = retryAfterCap
	}
	return d
}

// RetryAfterHint is the server's Retry-After value, if any, for display.
func (e *Error) RetryAfterHint() string { return e.RetryAfter }

// IsCase reports whether the problem is the documented case `slug` — the fragment
// of its `type` URL (https://docs.pdfik.net/error-codes#<slug>) or, for the
// errors that carry one, the `error` code spelled in upper snake case.
func (e *Error) IsCase(slug string) bool {
	if strings.HasSuffix(e.Type, "#"+slug) {
		return true
	}
	return e.Code != "" && strings.EqualFold(e.Code, strings.ReplaceAll(slug, "-", "_"))
}

// errorFrom builds an *Error from a non-success response. Non-JSON bodies (a
// proxy error page, a truncated response) are summarised, never dumped.
func errorFrom(resp *http.Response) *Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	apiErr := &Error{RetryAfter: resp.Header.Get("Retry-After")}
	if err := json.Unmarshal(raw, apiErr); err != nil || (apiErr.Title == "" && apiErr.Detail == nil) {
		text := strings.Join(strings.Fields(string(raw)), " ")
		switch {
		case strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html"):
			text = http.StatusText(resp.StatusCode) + " (non-JSON HTML response from the server or a proxy in front of it)"
		case len(text) > errorDetailLimit:
			text = truncateRunes(text, errorDetailLimit) + "…"
		}
		apiErr.Detail = text
	}
	// The 402 shapes carry their code inside the detail object.
	if d, ok := apiErr.Detail.(map[string]any); ok && apiErr.Code == "" {
		if code, ok := d["error"].(string); ok {
			apiErr.Code = code
		}
		for k, v := range d {
			if s, ok := v.(string); ok {
				d[k] = SanitizeText(s)
			}
		}
	}
	apiErr.Status = resp.StatusCode
	apiErr.Type = SanitizeText(apiErr.Type)
	apiErr.Title = SanitizeText(apiErr.Title)
	apiErr.Code = SanitizeText(apiErr.Code)
	apiErr.RetryAfter = SanitizeText(apiErr.RetryAfter)
	if s, ok := apiErr.Detail.(string); ok {
		apiErr.Detail = SanitizeText(s)
	}
	return apiErr
}

// RenderError: the job reached "failed" on the server. Code is the API's
// error_code (PAGE_TIMEOUT, SSRF_BLOCKED, …).
type RenderError struct {
	JobID string
	Code  string
}

func (e *RenderError) Error() string {
	code := e.Code
	if code == "" {
		code = "unknown"
	}
	return fmt.Sprintf("rendering failed (%s) — see https://docs.pdfik.net/error-codes", code)
}

// NotFinishedError: the job was still in progress when the caller's timeout
// ran out. The job keeps going on the server.
type NotFinishedError struct {
	JobID  string
	Status string
	After  time.Duration
}

func (e *NotFinishedError) Error() string {
	status := e.Status
	if status == "" {
		status = "in progress"
	}
	if e.After <= 0 {
		return fmt.Sprintf("job %s is still %s — check it later with: pdfik status %s && pdfik download %s",
			e.JobID, status, e.JobID, e.JobID)
	}
	return fmt.Sprintf("job %s still %s after %s — check it later with: pdfik status %s && pdfik download %s",
		e.JobID, status, e.After, e.JobID, e.JobID)
}

// truncateRunes cuts s to at most n runes, never inside a multi-byte character.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

// DownloadError: the stream broke after Bytes bytes. One of the file's three
// download attempts was consumed.
type DownloadError struct {
	JobID  string
	Bytes  int64
	Reason string
}

func (e *DownloadError) Error() string {
	return fmt.Sprintf("download interrupted after %s (%s) — one of the file's 3 download attempts was consumed; retry with: pdfik download %s",
		HumanBytes(e.Bytes), e.Reason, e.JobID)
}
