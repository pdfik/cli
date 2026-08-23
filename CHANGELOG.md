# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.1] — 2026-08-23

### Added
- Container images on every release: `ghcr.io/pdfik/cli:<version>`, `:<major.minor>`
  and `:latest`, for linux/amd64 and linux/arm64, built from the repository
  Dockerfile by the release workflow.

## [0.1.0] — 2026-08-22

First public release.

### Added
- `url-to-pdf`, `html-to-pdf` (file or stdin), `status`, `download`, `version`.
- Output handling: `-o DIR` (created if missing), `-f NAME`, `-o -` to stream
  the PDF to stdout; atomic writes (temp file + rename), never to a terminal.
- Page options: `--format` (A0–A6, Letter, Legal, Tabloid, Ledger),
  `--landscape`, `--margin` (CSS shorthand) and `--margin-<side>`,
  `--no-background`; `--test` for free end-to-end runs that return a sample
  PDF; `-q/--quiet`; `--timeout`.
- Documented exit codes: 0 ok, 1 failed, 2 usage, 3 rendering failed on the
  server, 4 not finished within `--timeout`, 130 interrupted. Ctrl-C cancels
  cleanly: no partial file is left behind and the in-flight job id is printed.
- `wkhtmltopdf` compatibility mode: every wkhtmltopdf flag is mapped, accepted
  with a warning, or refused with a reason — see `COMPATIBILITY.md`. Includes
  `--default-header`, `--replace`, `--title`, case-insensitive `[toPage]`-style
  placeholders, `pt` margins, `file://` input, and `--version`/`-h` probes for
  wrappers such as pdfkit and wicked_pdf.
- Client robustness: one `Idempotency-Key` per submission with retries on
  transport errors and 502/503/504 (a job is never charged twice), rate-limit
  aware polling (`Retry-After` honoured and capped), download stall detection
  that never trips on a slow consumer, retry of the free `download-busy`
  rejection, API-key stripping on cross-host and https→http redirects, refusal
  of plain-http API URLs, terminal-escape sanitization of server text.
- Static binaries for Linux/macOS/Windows with a checksums file, and a
  `FROM scratch` non-root container image.

[Unreleased]: https://github.com/pdfik/cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/pdfik/cli/releases/tag/v0.1.0
