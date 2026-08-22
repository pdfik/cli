package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/pdfik/cli/internal/api"
)

// jobIDArg validates the one positional argument of status/download.
func jobIDArg(rest []string) (string, error) {
	if len(rest) != 1 {
		return "", usagef("expected exactly one job id")
	}
	if !api.ValidJobID(rest[0]) {
		return "", usagef("%q does not look like a job id", rest[0])
	}
	return rest[0], nil
}

// cmdStatus prints the job state once: `status: done (test) (CODE), download expires …`.
func cmdStatus(ctx context.Context, args []string, std streams) error {
	var conn connFlags
	fs := newFlagSet("status")
	conn.register(fs)
	rest, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		usage(std.out)
		return nil
	}
	if err != nil {
		return err
	}
	jobID, err := jobIDArg(rest)
	if err != nil {
		return err
	}
	client, err := conn.newClient(std.err)
	if err != nil {
		return err
	}
	st, err := client.Status(ctx, jobID)
	if err != nil {
		return err
	}
	line := "status: " + st.Status
	if st.Test {
		line += " (test)"
	}
	if st.ErrorCode != "" {
		line += " (" + st.ErrorCode + ")"
	}
	if st.ExpiresAt != "" {
		line += ", download expires " + st.ExpiresAt
	}
	fmt.Fprintln(std.out, line)
	return nil
}

// cmdDownload fetches a finished job by id — the recovery path every wait
// error points at. The status is checked first so a queued or failed job does
// not consume one of the file's three download attempts.
func cmdDownload(ctx context.Context, args []string, std streams) error {
	var conn connFlags
	var output, fileName string
	var quiet bool
	fs := newFlagSet("download")
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&output, "output", "", "")
	fs.StringVar(&fileName, "f", "", "")
	fs.StringVar(&fileName, "file-name", "", "")
	fs.BoolVar(&quiet, "q", false, "")
	fs.BoolVar(&quiet, "quiet", false, "")
	conn.register(fs)
	rest, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		usage(std.out)
		return nil
	}
	if err != nil {
		return err
	}
	jobID, err := jobIDArg(rest)
	if err != nil {
		return err
	}
	if err := checkOutput(output, fileName, std.out); err != nil {
		return err
	}
	client, err := conn.newClient(std.err)
	if err != nil {
		return err
	}
	st, err := client.Status(ctx, jobID)
	if err != nil {
		return err
	}
	switch st.Status {
	case api.StatusDone:
	case api.StatusFailed:
		return &api.RenderError{JobID: jobID, Code: st.ErrorCode}
	default:
		return &api.NotFinishedError{JobID: jobID, Status: st.Status}
	}
	saved, err := saveJob(ctx, client, jobID, outputPath(output, fileName, jobID), std.out)
	if err != nil {
		return err
	}
	if saved != stdoutMarker {
		progress(std.err, quiet, "saved %s%s", saved, savedSummary(st, saved, 0))
	}
	return nil
}
