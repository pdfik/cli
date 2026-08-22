//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pdfik/cli/internal/apitest"
)

// A saved PDF gets ordinary document permissions — 0666 before the umask, what
// cp and curl give a new file — not os.CreateTemp's private 0600.
func TestSavedFileRespectsUmask(t *testing.T) {
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	want := os.FileMode(0o666 &^ umask)

	f := apitest.New(t)
	dir := t.TempDir()
	if r := execute(t, f, nil, "url-to-pdf", "https://example.com", "-o", dir, "-f", "p.pdf"); r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	fi, err := os.Stat(filepath.Join(dir, "p.pdf"))
	if err != nil || fi.Mode().Perm() != want {
		t.Fatalf("expected %v (0666 &^ umask %04o), got %v (%v)", want, umask, fi.Mode().Perm(), err)
	}
}
