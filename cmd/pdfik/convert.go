package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pdfik/cli/internal/api"
)

const defaultTimeout = 3 * time.Minute

type convertFlags struct {
	conn       connFlags
	page       pageOptions
	output     string
	fileName   string
	deliverURL string
	sync       bool
	test       bool
	quiet      bool
	timeout    time.Duration
}

func parseConvertFlags(cmd string, args []string) (convertFlags, []string, error) {
	cf := convertFlags{timeout: defaultTimeout}
	fs := newFlagSet(cmd)
	fs.StringVar(&cf.output, "o", "", "")
	fs.StringVar(&cf.output, "output", "", "")
	fs.StringVar(&cf.fileName, "f", "", "")
	fs.StringVar(&cf.fileName, "file-name", "", "")
	fs.StringVar(&cf.page.format, "format", "", "")
	fs.BoolVar(&cf.page.landscape, "landscape", false, "")
	fs.StringVar(&cf.page.margin, "margin", "", "")
	for i, side := range marginSides {
		fs.StringVar(&cf.page.marginSides[i], "margin-"+side, "", "")
	}
	fs.BoolVar(&cf.page.noBackground, "no-background", false, "")
	fs.StringVar(&cf.deliverURL, "deliver-url", "", "")
	fs.BoolVar(&cf.sync, "sync", false, "")
	fs.BoolVar(&cf.test, "test", false, "")
	fs.BoolVar(&cf.quiet, "q", false, "")
	fs.BoolVar(&cf.quiet, "quiet", false, "")
	fs.Var(durationFlag{&cf.timeout}, "timeout", "")
	cf.conn.register(fs)
	rest, err := parseInterleaved(fs, args)
	return cf, rest, err
}

// deliveryOption is the request's `delivery` object for --deliver-url. The API
// supports exactly one mode in v1: the caller's presigned PUT URL.
func deliveryOption(url string) map[string]any {
	return map[string]any{"mode": "presigned_put", "url": url}
}

// deliveredLocation is what the CLI reports for a delivered job: the
// --deliver-url value with the query string (the signature) stripped. It comes
// from the flag the caller just typed, never from the API — the job.finished
// webhook deliberately carries no address at all, because a presigned URL is a
// credential and PDFik does not record it.
func deliveredLocation(deliverURL string) string {
	if i := strings.IndexByte(deliverURL, '?'); i >= 0 {
		return deliverURL[:i]
	}
	return deliverURL
}

// cmdConvert implements url-to-pdf and html-to-pdf.
func cmdConvert(ctx context.Context, cmd string, args []string, std streams) error {
	cf, rest, err := parseConvertFlags(cmd, args)
	if errors.Is(err, flag.ErrHelp) {
		usage(std.out)
		return nil
	}
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("expected exactly one input argument")
	}
	input := rest[0]

	options, err := cf.page.build()
	if err != nil {
		return err
	}
	body := api.Submission{}
	if len(options) > 0 {
		body["options"] = options
	}
	if cf.test {
		body["test"] = true
	}
	if cf.deliverURL != "" {
		body["delivery"] = deliveryOption(cf.deliverURL)
	}

	// Settle the destination before submitting: a bad -o/-f must not cost a
	// render. Checks that cannot fail later come first, so a refused run never
	// leaves a freshly created directory behind.
	if err := checkDestination(cf.deliverURL, cf.sync, cf.output, cf.fileName, std.out); err != nil {
		return err
	}

	var submit func(context.Context, *api.Client) (api.Job, error)
	var sync syncFunc
	switch cmd {
	case "url-to-pdf":
		submit = func(ctx context.Context, c *api.Client) (api.Job, error) {
			return c.SubmitURL(ctx, input, body)
		}
		if cf.sync {
			sync = func(ctx context.Context, c *api.Client, w io.Writer) (api.SyncResult, error) {
				return c.RenderURLSync(ctx, input, body, w)
			}
		}
	case "html-to-pdf":
		progress(std.err, cf.quiet, "pdfik html-to-pdf: note: HTML input is sanitized by the API (styles/scripts stripped; relative assets unresolved) — url-to-pdf renders with full fidelity")
		html, err := readInput(input, std.in)
		if err != nil {
			return err
		}
		submit = func(ctx context.Context, c *api.Client) (api.Job, error) {
			return c.SubmitHTML(ctx, html, body)
		}
		if cf.sync {
			sync = func(ctx context.Context, c *api.Client, w io.Writer) (api.SyncResult, error) {
				return c.RenderHTMLSync(ctx, html, body, w)
			}
		}
	}

	client, err := cf.conn.newClient(std.err)
	if err != nil {
		return err
	}
	return renderAndSave(ctx, client, submit, func(jobID string) string {
		return outputPath(cf.output, cf.fileName, jobID, ".pdf")
	}, runOptions{timeout: cf.timeout, quiet: cf.quiet, test: cf.test, deliverURL: cf.deliverURL,
		sync: sync, output: cf.output, fileName: cf.fileName}, std)
}

// syncFunc performs one wait-mode request, streaming the finished file into w.
type syncFunc func(context.Context, *api.Client, io.Writer) (api.SyncResult, error)

// runOptions are the knobs of the submit → wait → save pipeline.
type runOptions struct {
	timeout time.Duration
	quiet   bool
	test    bool
	// ext is the command's output extension (".pdf" when empty; ".png"/".jpg"
	// for the screenshot commands).
	ext string
	// deliverURL, when set, means the server uploads the output straight to
	// the caller's storage: the download step is skipped entirely (the API
	// answers 404 for such jobs) and the delivered location is reported.
	deliverURL string
	// sync, when set, switches the pipeline to the API's wait mode: one
	// request that answers with the file itself — no polling, no download.
	sync syncFunc
	// output and fileName are the raw -o/-f values. The async path resolves
	// them through outputFor up front; the sync path cannot — the default
	// file name is the job id, which arrives only with the response headers.
	output, fileName string
}

// renderAndSave is the one pipeline behind every rendering command and the
// wkhtmltopdf mode: submit, report the job id, wait, download, summarise.
// outputFor resolves the destination once the job id is known.
func renderAndSave(ctx context.Context, client *api.Client,
	submit func(context.Context, *api.Client) (api.Job, error),
	outputFor func(jobID string) string, opts runOptions, std streams) error {

	if opts.sync != nil {
		return renderSyncAndSave(ctx, client, opts, std)
	}
	ext := opts.ext
	if ext == "" {
		ext = ".pdf"
	}
	job, err := submit(ctx, client)
	if err != nil {
		return err
	}
	suffix := ""
	if opts.test {
		sample := "PDF"
		if ext != ".pdf" {
			sample = "image"
		}
		suffix = " (test mode — free, returns a sample " + sample + ")"
	}
	progress(std.err, opts.quiet, "job %s queued%s", job.JobID, suffix)

	started := time.Now()
	st, err := client.Wait(ctx, job.JobID, opts.timeout)
	if err != nil {
		err = withJobHint(err, job.JobID)
		if opts.deliverURL != "" {
			err = deliveryHint(err, job.JobID, deliveredLocation(opts.deliverURL))
		}
		return err
	}
	if opts.deliverURL != "" {
		// The output went straight to the caller's storage; there is nothing
		// to download here. The location is the one the caller asked for,
		// minus the presigned query string.
		progress(std.err, opts.quiet, "job %s delivered: %s%s",
			job.JobID, deliveredLocation(opts.deliverURL), savedSummary(st, "", time.Since(started)))
		return nil
	}
	saved, err := saveJob(ctx, client, job.JobID, outputFor(job.JobID), ext, false, std.out)
	if err != nil {
		return withJobHint(err, job.JobID)
	}
	if saved != stdoutMarker {
		progress(std.err, opts.quiet, "saved %s%s", saved, savedSummary(st, saved, time.Since(started)))
	}
	return nil
}

// renderSyncAndSave is the pipeline's wait-mode branch: ONE request that
// answers with the file itself — no "queued" line (there is no job id until
// the response), no polling, no download step. The default file name IS the
// job id, which arrives only with the response headers, so the bytes stream
// into a partial file in the destination directory and the rename happens at
// the end — the same never-truncate guarantee as a download.
func renderSyncAndSave(ctx context.Context, client *api.Client, opts runOptions, std streams) error {
	ext := opts.ext
	if ext == "" {
		ext = ".pdf"
	}
	suffix := ""
	if opts.test {
		sample := "PDF"
		if ext != ".pdf" {
			sample = "image"
		}
		suffix = " (test mode — free, returns a sample " + sample + ")"
	}
	progress(std.err, opts.quiet, "rendering, waiting for the file%s", suffix)

	started := time.Now()
	// The partial file is anchored next to the real destination ("output"
	// stands in for the still-unknown job id), so the final rename stays
	// same-directory and atomic.
	anchor := outputPath(opts.output, opts.fileName, "output", ext)
	var res api.SyncResult
	saved, err := saveStream(anchor, func(string) string {
		return outputPath(opts.output, opts.fileName, res.JobID, ext)
	}, std.out, func(w io.Writer) (string, error) {
		var err error
		res, err = opts.sync(ctx, client, w)
		return res.ContentType, err
	})
	if res.Warning != "" {
		// The e-invoice profile note rides a response header in sync mode.
		// Printed outside progress(): -q silences chatter, not diagnostics.
		fmt.Fprintln(std.err, "pdfik: warning: "+res.Warning)
	}
	if err != nil {
		return syncHint(err, res.JobID)
	}
	if saved != stdoutMarker {
		progress(std.err, opts.quiet, "saved %s%s", saved, syncSummary(res, time.Since(started)))
	}
	return nil
}

// syncHint shapes a wait-mode failure. Before the response headers arrive
// there is no job id — an interrupt hint must not invent one — and the
// server's own sync timeout means the job no longer exists, so any
// `pdfik download` advice would only 404.
func syncHint(err error, jobID string) error {
	var st *api.SyncTimeoutError
	if errors.As(err, &st) {
		return err // already says: cancelled, nothing charged, retry or drop --sync
	}
	if errors.Is(err, context.Canceled) {
		if jobID == "" {
			return fmt.Errorf("interrupted while waiting for the render — no job id had arrived yet; if the job was created it keeps running on the server and shows up in the dashboard's recent jobs (%w)", err)
		}
		// Headers arrived, so the render finished; the file stays
		// downloadable for the normal 24 h.
		return fmt.Errorf("interrupted — job %s already finished on the server; fetch it with: pdfik download %s (%w)", jobID, jobID, err)
	}
	if jobID != "" && !strings.Contains(err.Error(), jobID) {
		return fmt.Errorf("%w (job %s)", err, jobID)
	}
	return err
}

// syncSummary is savedSummary's sibling for wait-mode runs: there is no
// JobStatus to quote, only what came back with the file.
func syncSummary(res api.SyncResult, wall time.Duration) string {
	parts := []string{"job " + res.JobID, api.HumanBytes(res.Bytes)}
	if wall > 0 {
		parts = append(parts, "total "+wall.Round(100*time.Millisecond).String())
	}
	return " — " + strings.Join(parts, ", ")
}

// withJobHint makes sure every failure after the submit names the job — under
// -q the "job … queued" line was never printed — and that an interrupted run
// tells the user how to get the PDF it paid for: the job keeps running on the
// server.
func withJobHint(err error, jobID string) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("interrupted — job %s keeps running on the server; fetch it later with: pdfik download %s (%w)", jobID, jobID, err)
	}
	if !strings.Contains(err.Error(), jobID) {
		return fmt.Errorf("%w (job %s)", err, jobID)
	}
	return err
}

// deliveryHintError is a wait failure of a --deliver-url run with the
// "download it later" advice rewritten: the API answers 404
// output-delivered-externally for such jobs, because the output lands in the
// caller's storage. Unwrap keeps report()'s exit codes (NotFinishedError → 4,
// context.Canceled → 130).
type deliveryHintError struct {
	msg   string
	cause error
}

func (e *deliveryHintError) Error() string { return e.msg }
func (e *deliveryHintError) Unwrap() error { return e.cause }

// deliveryHint rewrites the three recovery hints a wait failure can carry —
// the timeout (api.NotFinishedError), the repeated poll failure (api.Client.Wait)
// and the interruption (withJobHint) — to point at `pdfik status` and at the
// delivered location instead of `pdfik download`. An error without such a
// hint (a render failure, a rejected key) is returned unchanged.
func deliveryHint(err error, jobID, location string) error {
	orig := err.Error()
	msg := strings.ReplaceAll(orig, " && pdfik download "+jobID, "")
	msg = strings.ReplaceAll(msg, "fetch it later with: pdfik download "+jobID, "check it later with: pdfik status "+jobID)
	if msg == orig {
		return err
	}
	return &deliveryHintError{
		msg:   msg + "\n  the output goes to " + location + " when the job finishes — nothing is stored on PDFik's side to download",
		cause: err,
	}
}

// savedSummary renders the useful facts the API reports about a finished job
// as a one-line suffix: pages, size, render timing, download deadline, plus
// the wall-clock time this CLI spent from submit to saved file.
func savedSummary(st api.JobStatus, path string, wall time.Duration) string {
	var parts []string
	if pages, ok := st.Pages(); ok {
		unit := "pages"
		if pages == 1 {
			unit = "page"
		}
		parts = append(parts, fmt.Sprintf("%d %s", pages, unit))
	}
	var size int64 = -1
	if st.Metrics != nil && st.Metrics.FileSizeBytes != nil {
		size = *st.Metrics.FileSizeBytes
	} else if path != "" && path != stdoutMarker {
		if fi, err := os.Stat(path); err == nil {
			size = fi.Size()
		}
	}
	if size >= 0 {
		parts = append(parts, api.HumanBytes(size))
	}
	// Test-mode jobs report zero render time (nothing was rendered) — omit it.
	if st.Metrics != nil && st.Metrics.TotalDurationMs != nil && *st.Metrics.TotalDurationMs > 0 {
		t := "render " + formatMs(*st.Metrics.TotalDurationMs)
		if st.Metrics.PageLoadMs != nil && *st.Metrics.PageLoadMs > 0 {
			t += " (page load " + formatMs(*st.Metrics.PageLoadMs) + ")"
		}
		parts = append(parts, t)
	}
	if wall > 0 {
		parts = append(parts, "total "+wall.Round(100*time.Millisecond).String())
	}
	if st.ExpiresAt != "" {
		parts = append(parts, "download link expires "+st.ExpiresAt)
	}
	if st.Test {
		parts = append(parts, "test mode")
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}
