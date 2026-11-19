package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"

	"github.com/pdfik/cli/internal/api"
)

// cmdAccount prints the key's plan, current quota window, usage in it and
// limits (GET /account) to stdout, one "name: value" line each, so scripts can
// grep it. Creates no job and charges nothing — a quick way to check a key.
func cmdAccount(ctx context.Context, args []string, std streams) error {
	var conn connFlags
	fs := newFlagSet("account")
	conn.register(fs)
	rest, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		usage(std.out)
		return nil
	}
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usagef("account takes no arguments")
	}
	client, err := conn.newClient(std.err)
	if err != nil {
		return err
	}
	acc, err := client.GetAccount(ctx)
	if err != nil {
		return err
	}
	for _, line := range accountLines(acc) {
		fmt.Fprintln(std.out, line)
	}
	return nil
}

func accountLines(acc api.Account) []string {
	keyName := "(no name)"
	if acc.Key.Name != nil && *acc.Key.Name != "" {
		keyName = *acc.Key.Name
	}
	return []string{
		"plan: " + acc.Plan,
		"key: " + keyName,
		"window: " + acc.Window.Start + " - " + acc.Window.End,
		"documents: " + optionalCount(acc.Usage.Documents, "unknown") + " / " + optionalCount(acc.Limits.Documents, "unlimited"),
		"bytes: " + strconv.FormatInt(acc.Usage.Bytes, 10) + " / " + optionalCount(acc.Limits.Bytes, "unlimited"),
		"requests per minute: " + strconv.FormatInt(acc.Limits.RequestsPerMinute, 10),
		"concurrent jobs: " + strconv.FormatInt(acc.Limits.ConcurrentJobs, 10),
		"max file size bytes: " + strconv.FormatInt(acc.Limits.MaxFileSizeBytes, 10),
		"test mode: " + strconv.FormatInt(acc.TestMode.PerMinute, 10) + " per minute, " + strconv.FormatInt(acc.TestMode.PerDay, 10) + " per day",
	}
}

func optionalCount(v *int64, absent string) string {
	if v == nil {
		return absent
	}
	return strconv.FormatInt(*v, 10)
}
