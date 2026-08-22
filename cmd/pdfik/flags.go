package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pdfik/cli/internal/api"
)

// newFlagSet silences the stdlib's auto-generated dump: the flags carry no
// usage strings (the real help is usage()), and a dump would print the API
// key if the environment default were registered directly.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseInterleaved lets flags appear before AND after positional arguments
// (`pdfik url-to-pdf https://… --test -o x`): the stdlib parser stops at the
// first positional, so it is fed the remainder after collecting each one. A
// `--` terminator keeps its POSIX meaning: everything after it is positional,
// even on later rounds (the stdlib honours it within a single Parse only).
//
// flag.ErrHelp passes through untouched; every other parse problem is a usage
// error.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, &usageError{err}
		}
		rest := fs.Args()
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(positionals, rest...), nil
		}
		if len(rest) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
}

// durationFlag parses --timeout: a positive Go duration such as 90s or 3m. A
// bare number is refused with an example instead of the stdlib's parse error —
// wkhtmltopdf users think in seconds.
type durationFlag struct{ d *time.Duration }

func (f durationFlag) String() string {
	if f.d == nil {
		return ""
	}
	return f.d.String()
}

func (f durationFlag) Set(s string) error {
	d, err := parseTimeout(s)
	if err != nil {
		return err
	}
	*f.d = d
	return nil
}

func parseTimeout(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a valid duration (examples: 90s, 3m)", s)
	}
	return d, nil
}

// connFlags are the two settings every command needs to reach the API. Flags
// win over the environment.
type connFlags struct {
	apiKey string
	apiURL string
}

func (c *connFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.apiKey, "api-key", "", "")
	fs.StringVar(&c.apiURL, "api-url", "", "")
}

// extraClientOptions are appended to every client the commands build; tests
// use them to replace the clock so polling never sleeps on the wall clock.
var extraClientOptions []api.Option

// newClient resolves the environment fallbacks and builds the client. A
// plain-http base URL on a non-loopback host is allowed only with
// PDFIK_INSECURE_HTTP=1, and then warned about exactly once.
func (c connFlags) newClient(stderr io.Writer) (*api.Client, error) {
	key, base := c.apiKey, c.apiURL
	if key == "" {
		key = os.Getenv("PDFIK_API_KEY")
	}
	if base == "" {
		base = os.Getenv("PDFIK_API_URL")
	}
	opts := append([]api.Option{api.WithUserAgent("pdfik-cli/" + effectiveVersion())}, extraClientOptions...)
	if host, plain := api.PlainHTTPHost(base); plain && os.Getenv("PDFIK_INSECURE_HTTP") == "1" {
		opts = append(opts, api.WithInsecureHTTP())
		fmt.Fprintf(stderr, "pdfik: warning: sending the API key over plain http to %s (PDFIK_INSECURE_HTTP=1)\n", host)
	}
	client, err := api.New(base, key, opts...)
	if err != nil {
		return nil, &usageError{err}
	}
	return client, nil
}
