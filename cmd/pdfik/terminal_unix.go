//go:build !windows

package main

import (
	"io"
	"os"
)

// isTerminal reports whether w is an interactive terminal — the one place a
// PDF must never be written. /dev/null is a character device too and is a
// legitimate destination (`-o - > /dev/null`), so it is excluded explicitly.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}
