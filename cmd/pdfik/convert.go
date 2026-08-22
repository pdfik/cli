package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pdfik/cli/internal/api"
)

const defaultTimeout = 3 * time.Minute

type convertFlags struct {
	conn     connFlags
	page     pageOptions
	output   string
	fileName string
	test     bool
	quiet    bool
	timeout  time.Duration
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
	fs.BoolVar(&cf.test, "test", false, "")
	fs.BoolVar(&cf.quiet, "q", false, "")
	fs.BoolVar(&cf.quiet, "quiet", false, "")
	fs.Var(durationFlag{&cf.timeout}, "timeout", "")
	cf.conn.register(fs)
	rest, err := parseInterleaved(fs, args)
	return cf, rest, err
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

	// Settle the destination before submitting: a bad -o/-f must not cost a
	// render. Checks that cannot fail later come first, so a refused run never
	// leaves a freshly created directory behind.
	if err := checkOutput(cf.output, cf.fileName, std.out); err != nil {
		return err
	}

	var submit func(context.Context, *api.Client) (api.Job, error)
	switch cmd {
	case "url-to-pdf":
		submit = func(ctx context.Context, c *api.Client) (api.Job, error) {
			return c.SubmitURL(ctx, input, body)
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
	}

	client, err := cf.conn.newClient(std.err)
	if err != nil {
		return err
	}
	return renderAndSave(ctx, client, submit, func(jobID string) string {
		return outputPath(cf.output, cf.fileName, jobID)
	}, runOptions{timeout: cf.timeout, quiet: cf.quiet, test: cf.test}, std)
}

// runOptions are the knobs of the submit → wait → save pipeline.
type runOptions struct {
	timeout time.Duration
	quiet   bool
	test    bool
}

// renderAndSave is the one pipeline behind url-to-pdf, html-to-pdf and the
// wkhtmltopdf mode: submit, report the job id, wait, download, summarise.
// outputFor resolves the destination once the job id is known.
func renderAndSave(ctx context.Context, client *api.Client,
	submit func(context.Context, *api.Client) (api.Job, error),
	outputFor func(jobID string) string, opts runOptions, std streams) error {

	job, err := submit(ctx, client)
	if err != nil {
		return err
	}
	suffix := ""
	if opts.test {
		suffix = " (test mode — free, returns a sample PDF)"
	}
	progress(std.err, opts.quiet, "job %s queued%s", job.JobID, suffix)

	started := time.Now()
	st, err := client.Wait(ctx, job.JobID, opts.timeout)
	if err != nil {
		return withJobHint(err, job.JobID)
	}
	saved, err := saveJob(ctx, client, job.JobID, outputFor(job.JobID), std.out)
	if err != nil {
		return withJobHint(err, job.JobID)
	}
	if saved != stdoutMarker {
		progress(std.err, opts.quiet, "saved %s%s", saved, savedSummary(st, saved, time.Since(started)))
	}
	return nil
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
