package main

import (
	"runtime/debug"
	"strings"
)

// Overridden at build time (release/Docker builds), without a leading "v":
//
//	go build -ldflags "-X main.version=0.1.0"
var version = "dev"

// effectiveVersion resolves what `pdfik version` and the User-Agent report.
// Release builds set main.version via ldflags and win outright. `go install
// module@version` builds carry the module version (v0.1.0) in the build info
// instead, so go-install users report the same "0.1.0". A plain `go build`
// reports "(devel)" (or, from Go 1.24 on, a VCS-derived pseudo-version) — the
// former stays "dev", the latter is shown as is.
func effectiveVersion() string {
	if version != "dev" {
		return strings.TrimPrefix(version, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}
