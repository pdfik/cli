package main

import (
	"context"
	"errors"
	"flag"
	"strconv"
	"strings"
	"time"

	"github.com/pdfik/cli/internal/api"
)

// Viewport bounds the API accepts (CSS pixels) — the same range it validates
// against. Checking locally gives an instant, specific message instead of a
// 422 round-trip. Width and height differ: a capture is as wide as a desktop
// at most, but may be as tall as a long page.
const (
	viewportMin       = 320
	viewportMaxWidth  = 1920
	viewportMaxHeight = 8192
)

type screenshotFlags struct {
	conn       connFlags
	output     string
	fileName   string
	format     string
	fullPage   bool
	quality    string
	viewport   string
	deliverURL string
	test       bool
	quiet      bool
	timeout    time.Duration
}

func parseScreenshotFlags(cmd string, args []string) (screenshotFlags, []string, error) {
	sf := screenshotFlags{timeout: defaultTimeout}
	fs := newFlagSet(cmd)
	fs.StringVar(&sf.output, "o", "", "")
	fs.StringVar(&sf.output, "output", "", "")
	fs.StringVar(&sf.fileName, "f", "", "")
	fs.StringVar(&sf.fileName, "file-name", "", "")
	fs.StringVar(&sf.format, "format", "", "")
	fs.BoolVar(&sf.fullPage, "full-page", false, "")
	fs.StringVar(&sf.quality, "quality", "", "")
	fs.StringVar(&sf.viewport, "viewport", "", "")
	fs.StringVar(&sf.deliverURL, "deliver-url", "", "")
	fs.BoolVar(&sf.test, "test", false, "")
	fs.BoolVar(&sf.quiet, "q", false, "")
	fs.BoolVar(&sf.quiet, "quiet", false, "")
	fs.Var(durationFlag{&sf.timeout}, "timeout", "")
	sf.conn.register(fs)
	rest, err := parseInterleaved(fs, args)
	return sf, rest, err
}

// resolveImageFormat validates --format and returns the API's canonical name
// plus the file extension the output gets. "" means the API default (png);
// "jpg" is accepted as an alias for "jpeg", as the API does.
func resolveImageFormat(f string) (format, ext string, err error) {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "", "png":
		return "png", ".png", nil
	case "jpeg", "jpg":
		return "jpeg", ".jpg", nil
	}
	return "", "", usagef("image format %q is not supported (supported: png, jpeg)", f)
}

// parseViewport parses --viewport WIDTHxHEIGHT (CSS pixels, e.g. 1024x768).
func parseViewport(s string) (int, int, error) {
	bad := func() (int, int, error) {
		return 0, 0, usagef("--viewport %q must be WIDTHxHEIGHT in CSS pixels, width %d-%d and height %d-%d (e.g. 1024x768)",
			s, viewportMin, viewportMaxWidth, viewportMin, viewportMaxHeight)
	}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "x")
	if len(parts) != 2 {
		return bad()
	}
	w, werr := strconv.Atoi(parts[0])
	h, herr := strconv.Atoi(parts[1])
	if werr != nil || herr != nil {
		return bad()
	}
	if w < viewportMin || w > viewportMaxWidth || h < viewportMin || h > viewportMaxHeight {
		return bad()
	}
	return w, h, nil
}

// buildImageOptions turns the screenshot flags into the API's `options`
// object (nil when nothing was set, so the API's defaults stay the API's
// business) and resolves the output extension. Refusals mirror the API's own
// validation, so nothing is charged for a doomed request.
func (sf screenshotFlags) buildImageOptions() (map[string]any, string, error) {
	format, ext, err := resolveImageFormat(sf.format)
	if err != nil {
		return nil, "", err
	}
	opts := map[string]any{}
	if sf.format != "" {
		opts["format"] = format
	}
	if sf.fullPage {
		opts["full_page"] = true
	}
	if sf.quality != "" {
		q, err := strconv.Atoi(sf.quality)
		if err != nil || q < 1 || q > 100 {
			return nil, "", usagef("--quality %q must be a whole number between 1 and 100", sf.quality)
		}
		if format != "jpeg" {
			return nil, "", usagef("--quality applies to jpeg only — add --format jpeg or drop it")
		}
		opts["quality"] = q
	}
	if sf.viewport != "" {
		w, h, err := parseViewport(sf.viewport)
		if err != nil {
			return nil, "", err
		}
		opts["viewport"] = map[string]any{"width": w, "height": h}
	}
	if len(opts) == 0 {
		return nil, ext, nil
	}
	return opts, ext, nil
}

// cmdScreenshot implements url-to-image and html-to-image: the same submit →
// wait → save pipeline as the PDF commands, with a PNG or JPEG at the end.
func cmdScreenshot(ctx context.Context, cmd string, args []string, std streams) error {
	sf, rest, err := parseScreenshotFlags(cmd, args)
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

	options, ext, err := sf.buildImageOptions()
	if err != nil {
		return err
	}
	body := api.Submission{}
	if options != nil {
		body["options"] = options
	}
	if sf.test {
		body["test"] = true
	}
	if sf.deliverURL != "" {
		body["delivery"] = deliveryOption(sf.deliverURL)
	}

	// Settle the destination before submitting: a bad -o/-f must not cost a
	// render.
	if err := checkDestination(sf.deliverURL, sf.output, sf.fileName, std.out); err != nil {
		return err
	}

	var submit func(context.Context, *api.Client) (api.Job, error)
	switch cmd {
	case "url-to-image":
		submit = func(ctx context.Context, c *api.Client) (api.Job, error) {
			return c.SubmitURLImage(ctx, input, body)
		}
	case "html-to-image":
		progress(std.err, sf.quiet, "pdfik html-to-image: note: HTML input is sanitized by the API (styles/scripts stripped; relative assets unresolved) — url-to-image renders with full fidelity")
		html, err := readInput(input, std.in)
		if err != nil {
			return err
		}
		submit = func(ctx context.Context, c *api.Client) (api.Job, error) {
			return c.SubmitHTMLImage(ctx, html, body)
		}
	}

	client, err := sf.conn.newClient(std.err)
	if err != nil {
		return err
	}
	return renderAndSave(ctx, client, submit, func(jobID string) string {
		return outputPath(sf.output, sf.fileName, jobID, ext)
	}, runOptions{timeout: sf.timeout, quiet: sf.quiet, test: sf.test, ext: ext, deliverURL: sf.deliverURL}, std)
}
