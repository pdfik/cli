package main

import (
	"context"
	"errors"
	"flag"

	"github.com/pdfik/cli/internal/api"
)

// cmdMarkdown implements markdown-to-pdf: CommonMark + GFM tables and
// strikethrough in, a PDF out, through the same submit → wait → save pipeline
// — and with the same page flags — as url-to-pdf/html-to-pdf. Raw HTML inside
// the Markdown is escaped by the API, not rendered.
func cmdMarkdown(ctx context.Context, args []string, std streams) error {
	cf, rest, err := parseConvertFlags("markdown-to-pdf", args)
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
	// render.
	if err := checkDestination(cf.deliverURL, cf.output, cf.fileName, std.out); err != nil {
		return err
	}

	markdown, err := readInput(input, std.in)
	if err != nil {
		return err
	}
	submit := func(ctx context.Context, c *api.Client) (api.Job, error) {
		return c.SubmitMarkdown(ctx, markdown, body)
	}

	client, err := cf.conn.newClient(std.err)
	if err != nil {
		return err
	}
	return renderAndSave(ctx, client, submit, func(jobID string) string {
		return outputPath(cf.output, cf.fileName, jobID, ".pdf")
	}, runOptions{timeout: cf.timeout, quiet: cf.quiet, test: cf.test, deliverURL: cf.deliverURL}, std)
}
