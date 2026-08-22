//go:build windows

package main

import (
	"io"
	"os"
	"syscall"
)

// isTerminal reports whether w is an interactive console — the one place a
// PDF must never be written. Only a console handle answers GetConsoleMode;
// NUL, pipes and files do not.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}
