package main

import (
	"context"
	"errors"
	"flag"
	"strings"
	"time"

	"github.com/pdfik/cli/internal/api"
)

// einvoiceProfiles are the Factur-X conformance profiles the API accepts —
// the same list it validates against. Checking locally gives an instant,
// specific message instead of a 422 round-trip.
var einvoiceProfiles = []string{"minimum", "basicwl", "basic", "en16931", "extended"}

// validateProfile accepts the supported profile names (exact, lower-case —
// the API is case-sensitive here).
func validateProfile(p string) error {
	for _, s := range einvoiceProfiles {
		if s == p {
			return nil
		}
	}
	return usagef("profile %q is not supported (supported: %s)", p, strings.Join(einvoiceProfiles, ", "))
}

type einvoiceFlags struct {
	conn     connFlags
	output   string
	fileName string
	profile  string
	template string
	webhook  string
	test     bool
	quiet    bool
	timeout  time.Duration
}

func parseEInvoiceFlags(args []string) (einvoiceFlags, []string, error) {
	ef := einvoiceFlags{timeout: defaultTimeout}
	fs := newFlagSet("einvoice-to-pdf")
	fs.StringVar(&ef.output, "o", "", "")
	fs.StringVar(&ef.output, "output", "", "")
	fs.StringVar(&ef.fileName, "f", "", "")
	fs.StringVar(&ef.fileName, "file-name", "", "")
	fs.StringVar(&ef.profile, "profile", "", "")
	fs.StringVar(&ef.template, "template", "", "")
	fs.StringVar(&ef.webhook, "webhook", "", "")
	fs.BoolVar(&ef.test, "test", false, "")
	fs.BoolVar(&ef.quiet, "q", false, "")
	fs.BoolVar(&ef.quiet, "quiet", false, "")
	fs.Var(durationFlag{&ef.timeout}, "timeout", "")
	ef.conn.register(fs)
	rest, err := parseInterleaved(fs, args)
	return ef, rest, err
}

// cmdEInvoice implements einvoice-to-pdf: UN/CEFACT CII XML in, a Factur-X
// (PDF/A-3) hybrid e-invoice out, through the same submit → wait → save
// pipeline as the other commands. The visual half comes from a block template
// (--template or the account default), so the page-option flags do not apply.
func cmdEInvoice(ctx context.Context, args []string, std streams) error {
	ef, rest, err := parseEInvoiceFlags(args)
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

	body := api.Submission{}
	if ef.profile != "" {
		if err := validateProfile(ef.profile); err != nil {
			return err
		}
		body["profile"] = ef.profile
	}
	if ef.template != "" {
		body["template_id"] = ef.template
	}
	if ef.webhook != "" {
		body["webhook_url"] = ef.webhook
	}
	if ef.test {
		body["test"] = true
	}

	// Settle the destination before submitting: a bad -o/-f must not cost a
	// render.
	if err := checkOutput(ef.output, ef.fileName, std.out); err != nil {
		return err
	}

	if ef.profile == "minimum" || ef.profile == "basicwl" {
		progress(std.err, ef.quiet, "pdfik einvoice-to-pdf: note: profile %q embeds accompanying data only — the result is NOT a legally sufficient e-invoice; use basic, en16931 or extended for a full invoice", ef.profile)
	}

	xml, err := readInput(input, std.in)
	if err != nil {
		return err
	}
	submit := func(ctx context.Context, c *api.Client) (api.Job, error) {
		return c.SubmitEInvoice(ctx, xml, body)
	}

	client, err := ef.conn.newClient(std.err)
	if err != nil {
		return err
	}
	return renderAndSave(ctx, client, submit, func(jobID string) string {
		return outputPath(ef.output, ef.fileName, jobID)
	}, runOptions{timeout: ef.timeout, quiet: ef.quiet, test: ef.test}, std)
}
