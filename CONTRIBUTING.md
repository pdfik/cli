# Contributing

Thanks for helping make `pdfik` better. This is a small, dependency-free Go
program; the bar is correctness and clarity, not cleverness.

## Development

```bash
git clone https://github.com/pdfik/cli && cd cli
go build ./cmd/pdfik          # produces ./pdfik
go test -race ./...           # unit tests — no network, no API key needed
go vet ./... && gofmt -l .    # must be clean
```

Run it against the real API with your own key:

```bash
export PDFIK_API_KEY=sk_live_...
./pdfik url-to-pdf https://example.com --test   # --test is free: no quota used
```

## Ground rules

- **Standard library only.** Zero dependencies is a feature (supply chain,
  reproducible builds, `go install` just works). A PR that adds a module needs a
  very good reason.
- **Fail before the render is charged.** Anything that can be validated locally
  (formats, margin units, output paths, flag combinations) must be validated
  before the job is submitted — a render costs the user a quota unit.
- **Never leak the API key.** Not in `argv`, not in logs, not in error messages,
  not to a foreign host on redirect. Tests exist for the last one; keep them green.
- **Honest wkhtmltopdf mode.** A wkhtmltopdf flag is either *mapped* (same
  meaning), *ignored with a warning* (no effect on this pipeline), or *refused
  with a reason* (it would change the output). Never silently accepted.
  Update `COMPATIBILITY.md` in the same change.
- **stdout is for PDFs.** Everything human-readable goes to stderr so
  `pdfik ... -o -` can be piped.

## Pull requests

1. Open an issue first for anything bigger than a bug fix, so the design can be
   discussed before the code.
2. Add or extend tests — the client is exercised through the fake API in
   `internal/apitest`, the flag parsers through table-driven tests.
3. Keep commits focused; write the commit message for the person reading
   `git log` in a year.
4. CI must pass (`gofmt`, `go vet`, `staticcheck`, `go test -race` on Linux,
   macOS and Windows).

## How changes land

This repository is a mirror: the code is developed in the PDFik platform
monorepo and synced here, and releases are cut from there. Pull requests are
reviewed here; an accepted change is applied upstream, credited to you in
`CHANGELOG.md`, and appears in the next sync commit — your PR is then closed
with a link to that commit rather than merged directly. The sync refuses to run
while this repository has commits it does not know about, so nothing merged
here can be overwritten by accident. Dependabot runs on this repository in
advisory mode only (security alerts, no pull requests); toolchain and action
updates are applied upstream.

## Reporting security issues

Please do not open a public issue — see [SECURITY.md](SECURITY.md).
