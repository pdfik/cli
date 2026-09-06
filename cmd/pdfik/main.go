// Command pdfik renders URLs and HTML to PDF through the PDFik cloud API.
//
// Commands: url-to-pdf, html-to-pdf, einvoice-to-pdf, wkhtmltopdf
// (compatibility mode), status, download, version, help. Everything
// human-readable goes to stderr; stdout carries only a PDF (`-o -`) or the
// `status` line, so the tool pipes cleanly.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/pdfik/cli/internal/api"
)

// Exit codes are part of the contract — shell scripts branch on them. They are
// documented in `pdfik help` and in README.md; keep the three in sync.
const (
	exitOK           = 0
	exitFailure      = 1   // the request, render or download failed (API, network, file)
	exitUsage        = 2   // invalid usage or a value refused before anything was submitted
	exitRenderFailed = 3   // the job failed on the server; the error code was printed
	exitNotFinished  = 4   // the job was not finished within --timeout; the job id was printed
	exitInterrupted  = 130 // SIGINT / SIGTERM
)

// streams are the process's standard streams, injected so tests can drive the
// program end to end.
type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal cancels the context and cleans up; a second one while
	// that is still in progress must kill the process the ordinary way.
	go func() {
		<-ctx.Done()
		stop()
	}()
	os.Exit(run(ctx, os.Args[1:], streams{in: os.Stdin, out: os.Stdout, err: os.Stderr}))
}

func run(ctx context.Context, args []string, std streams) int {
	if len(args) == 0 {
		usage(std.err)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "url-to-pdf", "html-to-pdf":
		err = cmdConvert(ctx, cmd, rest, std)
	case "einvoice-to-pdf":
		err = cmdEInvoice(ctx, rest, std)
	case "wkhtmltopdf":
		err = cmdWkhtmltopdf(ctx, rest, std)
	case "status":
		err = cmdStatus(ctx, rest, std)
	case "download":
		err = cmdDownload(ctx, rest, std)
	case "version", "--version", "-v":
		fmt.Fprintln(std.out, "pdfik "+effectiveVersion())
		return exitOK
	case "help", "--help", "-h":
		usage(std.out)
		return exitOK
	default:
		fmt.Fprintf(std.err, "pdfik: unknown command %q\n\n", cmd)
		usage(std.err)
		return exitUsage
	}
	return report(std.err, "pdfik "+cmd, err)
}

// usageError marks problems with the invocation itself — flags, arguments,
// paths, configuration — that are detected before anything is submitted.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usagef(format string, a ...any) error { return &usageError{fmt.Errorf(format, a...)} }

// report is the single place that prints an error and maps it to an exit code.
func report(stderr io.Writer, prefix string, err error) int {
	if err == nil {
		return exitOK
	}
	var ue *usageError
	if errors.As(err, &ue) {
		help := "pdfik help"
		if prefix == "pdfik wkhtmltopdf" {
			help = "pdfik wkhtmltopdf --help"
		}
		fmt.Fprintf(stderr, "%s: %s\nRun '%s' for usage.\n", prefix, ue.Error(), help)
		return exitUsage
	}
	fmt.Fprintf(stderr, "%s: %s\n", prefix, api.SanitizeText(err.Error()))
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Status == 429 && apiErr.RetryAfterHint() != "" {
		fmt.Fprintf(stderr, "  retry after: %s\n", apiErr.RetryAfterHint())
	}
	switch {
	case errors.Is(err, context.Canceled):
		return exitInterrupted
	case errors.As(err, new(*api.RenderError)):
		return exitRenderFailed
	case errors.As(err, new(*api.NotFinishedError)):
		return exitNotFinished
	}
	return exitFailure
}

// progress prints a status line unless the user asked for quiet. Warnings and
// errors never go through here — -q silences chatter, not diagnostics.
func progress(w io.Writer, quiet bool, format string, a ...any) {
	if quiet {
		return
	}
	fmt.Fprintf(w, format+"\n", a...)
}

func usage(w io.Writer) {
	fmt.Fprint(w, `pdfik — render URLs and HTML to PDF via the PDFik cloud API

Usage:
  pdfik url-to-pdf      <url>              [flags]
  pdfik html-to-pdf     <file.html | ->    [flags]
  pdfik einvoice-to-pdf <invoice.xml | ->  [flags]
  pdfik wkhtmltopdf [wkhtmltopdf-flags] <input> <output>
  pdfik download    <job-id>         [flags]
  pdfik status      <job-id>
  pdfik version

Flags:
  -o, --output DIR      output directory (default: current directory; created if missing);
                        '-' streams the PDF to stdout
  -f, --file-name NAME  file name inside the output directory (default: <job-id>.pdf)
      --format SIZE     page format: A4 (default), A0-A6, Letter, Legal, Tabloid, Ledger
      --landscape       landscape orientation
      --margin VALUE    page margins, each with a unit (mm, cm, in, px): one value
                        for all sides, or CSS shorthand top,right,bottom,left
                        (2 = vertical,horizontal; 3 = top,horizontal,bottom)
      --margin-top VALUE, --margin-right, --margin-bottom, --margin-left
                        one side; overrides --margin for that side
      --no-background   skip CSS backgrounds (they print by default)
      --profile NAME    einvoice-to-pdf: Factur-X profile the XML declares:
                        minimum, basicwl, basic, en16931 (default), extended
      --template ID     einvoice-to-pdf: id of a saved invoice template
                        (Dashboard → E-Invoice; default: the account default)
      --webhook URL     einvoice-to-pdf: callback URL POSTed when the job finishes
      --test            free test run: full pipeline, sample PDF, no quota used
      --timeout DUR     how long to wait for rendering (default 3m; e.g. 90s)
  -q, --quiet           no progress lines (warnings and errors still print)
      --api-key KEY     API key; prefer $PDFIK_API_KEY — flag values are visible
                        to other local processes and shell history
      --api-url URL     API base URL (default: $PDFIK_API_URL or https://api.pdfik.net)

Environment:
  PDFIK_API_KEY         API key (preferred over --api-key)
  PDFIK_API_URL         API base URL (the flag wins)
  PDFIK_INSECURE_HTTP=1 allow plain http to a non-loopback host (lab use; warns)

Exit codes:
  0  PDF written        1  request, render or download failed (API, network, file)
  2  usage error        3  rendering failed on the server (error code printed)
  4  not finished within --timeout (job id printed — fetch it later with
     'pdfik download <job-id>')                                   130  interrupted

Note: html-to-pdf input is sanitized by the API (styles/scripts stripped) —
use url-to-pdf for styled documents.
einvoice-to-pdf builds a Factur-X (PDF/A-3) e-invoice from UN/CEFACT CII XML;
its layout comes from an invoice template, so the page flags do not apply.
An API key is required — create one at https://pdfik.net/dashboard/api-keys.
Docs: https://docs.pdfik.net
`)
}
