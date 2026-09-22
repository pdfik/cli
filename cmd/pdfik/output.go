package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdfik/cli/internal/api"
)

// stdoutMarker is the -o value that streams the PDF to stdout.
const stdoutMarker = "-"

// Replaceable in tests.
var (
	renameFile       = os.Rename
	stdoutIsTerminal = isTerminal
)

// checkDestination settles where the output goes before anything is
// submitted. With --deliver-url the server uploads the output straight to the
// caller's own storage and nothing is downloaded, so a local destination is
// refused rather than silently ignored.
func checkDestination(deliverURL, dir, fileName string, stdout io.Writer) error {
	if deliverURL == "" {
		return checkOutput(dir, fileName, stdout)
	}
	if dir != "" || fileName != "" {
		return usagef("-o/--output and -f/--file-name cannot be combined with --deliver-url — the output is uploaded to your storage and nothing is downloaded")
	}
	return nil
}

// checkOutput validates -o/-f before anything is submitted, in an order that
// never leaves side effects behind a refusal: the file name and the terminal
// guard first, the directory (which may be created) last.
func checkOutput(dir, fileName string, stdout io.Writer) error {
	if dir == stdoutMarker {
		if stdoutIsTerminal(stdout) {
			return usagef("refusing to write binary output to a terminal — redirect stdout or use -o DIR")
		}
		return nil
	}
	if err := checkFileName(fileName); err != nil {
		return err
	}
	return prepareOutputDir(dir, "-o/--output")
}

// outputExtensions are the file extensions the commands produce: .pdf for the
// PDF commands, .png/.jpg for the screenshot commands.
var outputExtensions = []string{".pdf", ".png", ".jpg", ".jpeg"}

// looksLikeFileName reports whether a directory argument was actually given an
// output file name (the old `-o x.pdf` habit, and its .png/.jpg siblings).
func looksLikeFileName(dir string) bool {
	lower := strings.ToLower(dir)
	for _, ext := range outputExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// prepareOutputDir makes sure the output directory exists. "" means the
// current directory. Passing a file path here (the old `-o x.pdf` habit) is
// refused with a pointer to -f, so a directory named "x.pdf" is never created.
func prepareOutputDir(dir, label string) error {
	if dir == "" {
		dir = "."
	}
	hint := ""
	if label == "-o/--output" {
		hint = " — use -f/--file-name for the file name"
	}
	fi, err := os.Stat(dir)
	switch {
	case err == nil && !fi.IsDir():
		return usagef("%s %q is a file; it must be a directory%s", label, dir, hint)
	case err == nil:
		return nil
	case looksLikeFileName(dir):
		return usagef("%s %q looks like a file name; it must be a directory%s", label, dir, hint)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return usagef("cannot create output directory %s: %v", dir, err)
	}
	return nil
}

// checkFileName enforces the -f contract: a bare name, no directories.
func checkFileName(name string) error {
	if strings.ContainsAny(name, `/\`) {
		return usagef("-f/--file-name %q must be a bare file name — put the directory in -o/--output", name)
	}
	return nil
}

// outputPath is the final destination for a job: <dir>/<name>, with
// <job-id><ext> as the default name — unique per run and the same id that
// `pdfik download` takes. ext is the command's output kind: ".pdf" for the
// PDF commands, ".png"/".jpg" for the screenshot commands.
func outputPath(dir, fileName, jobID, ext string) string {
	if dir == stdoutMarker {
		return stdoutMarker
	}
	if dir == "" {
		dir = "."
	}
	if fileName == "" {
		fileName = jobID + ext
	}
	return filepath.Join(dir, fileName)
}

// extForMedia maps a download's Content-Type to the file extension of that
// output kind; "" for a type the CLI does not know.
func extForMedia(contentType string) string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch mediaType {
	case "application/pdf":
		return ".pdf"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	}
	return ""
}

// saveJob downloads the finished output to a file or, for "-", to stdout, and
// returns where it went. ext names the default extension when output turns
// out to be a directory.
//
// autoExt means the file name is the CLI's own <job-id><ext>, not the user's:
// the extension then follows the download's Content-Type. `pdfik download`
// cannot know the job's kind from its id, and naming a screenshot .pdf would
// hand the user a file no viewer opens.
//
// File downloads stream into a temp file next to the destination and rename
// over it only on success: a failed run can never truncate or delete a file
// from an earlier run, an interrupted run leaves the previous file intact,
// and the same-directory rename is atomic. The temp file is closed BEFORE
// removal — Windows cannot delete an open file.
func saveJob(ctx context.Context, client *api.Client, jobID, output, ext string, autoExt bool, stdout io.Writer) (string, error) {
	if output == stdoutMarker {
		if stdoutIsTerminal(stdout) { // backstop; checkOutput refused this before the submit
			return "", usagef("refusing to write binary output to a terminal — redirect stdout or use -o DIR")
		}
		if _, _, err := client.Download(ctx, jobID, stdout); err != nil {
			return "", err
		}
		return stdoutMarker, nil
	}
	// wkhtmltopdf mode passes its positional <output> straight here; an existing
	// directory means "name the file after the job id inside it".
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		output = filepath.Join(output, jobID+ext)
		autoExt = true
	}
	tmp, err := createPartial(output)
	if err != nil {
		return "", err
	}
	_, contentType, err := client.Download(ctx, jobID, tmp)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if autoExt {
		// The temp file stays where it is: same directory, so the rename below
		// is still atomic.
		if actual := extForMedia(contentType); actual != "" && actual != ext && strings.HasSuffix(output, ext) {
			output = strings.TrimSuffix(output, ext) + actual
		}
	}
	if err := renameFile(tmp.Name(), output); err != nil {
		// Keep the temp file: the render is paid for and complete; only the
		// final rename failed (target open in a viewer, permissions…).
		return "", fmt.Errorf("could not write %s (%v) — is it open in another program? The output was kept at %s", output, underlying(err), tmp.Name())
	}
	return output, nil
}

// createPartial opens <output>.partial-<random> next to the destination with
// ordinary document permissions (0666 before the umask — what cp and curl
// give a new file), unlike os.CreateTemp's private 0600.
func createPartial(output string) (*os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var suffix [4]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		name := output + ".partial-" + hex.EncodeToString(suffix[:])
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not create a temporary file next to %s", output)
}

// underlying strips Go's "open <path>:" / "rename A B:" prefixes so a message
// can name the path once.
func underlying(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}
