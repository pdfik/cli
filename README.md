# pdfik CLI

> **Where this code lives:** developed in the PDFik platform monorepo; this tree is its commit `87ff87d`.
> Issues and PRs are welcome here; accepted PRs are applied upstream and land with the next sync (see CONTRIBUTING.md).

[![CI](https://github.com/pdfik/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/pdfik/cli/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/pdfik/cli.svg)](https://pkg.go.dev/github.com/pdfik/cli)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

wkhtmltopdf-compatible command-line client for [PDFik](https://pdfik.net) —
render URLs and HTML to PDF through the cloud API. One static binary, standard
library only, no dependencies; also a few-megabyte container image.

An API key is required: the [Free plan](https://pdfik.net/pricing) is enough to
use the CLI, and `--test` runs are free on every plan.

## Install

- **Release binaries** for Linux (amd64, arm64), macOS (amd64, arm64) and
  Windows (amd64): [github.com/pdfik/cli/releases](https://github.com/pdfik/cli/releases).

  ```bash
  curl -LO https://github.com/pdfik/cli/releases/latest/download/pdfik-linux-amd64
  curl -LO https://github.com/pdfik/cli/releases/latest/download/checksums.txt
  sha256sum --ignore-missing -c checksums.txt   # macOS: shasum -a 256 --ignore-missing -c checksums.txt
  chmod +x pdfik-linux-amd64 && sudo mv pdfik-linux-amd64 /usr/local/bin/pdfik
  ```
- **Go 1.23 or newer:** `go install github.com/pdfik/cli/cmd/pdfik@latest`
  (pin a version with `@v0.1.0`).
- **Docker:** `docker pull ghcr.io/pdfik/cli:0.1.1` — linux/amd64 and linux/arm64;
  `:0.1` and `:latest` track releases. How to run it (`--user`, mounting the
  output directory) is in the [Dockerfile](Dockerfile), which also builds
  locally with `docker build -t pdfik .`.
- **From source:** `go build -o pdfik ./cmd/pdfik`.

## Quick start

```bash
export PDFIK_API_KEY=sk_live_YOUR_API_KEY   # https://pdfik.net/dashboard/api-keys

pdfik url-to-pdf https://example.com                            # saves ./<job-id>.pdf
pdfik url-to-pdf https://example.com -o ./pdfs -f example.pdf   # -o directory, -f file name
pdfik html-to-pdf invoice.html -f invoice.pdf --format A4 --margin 10mm --margin-top 25mm
pdfik url-to-pdf https://example.com --test                     # free: full pipeline, sample PDF
pdfik url-to-pdf https://example.com -o -  > example.pdf        # stream the PDF to stdout
pdfik status   <job-id>
pdfik download <job-id> -o ./pdfs                               # fetch a finished job later
```

**`html-to-pdf` input is sanitized by the API** (`<style>`, `<script>` and
`class` attributes are stripped server-side; relative assets are not resolved).
**`url-to-pdf` renders with full fidelity** — prefer it for styled documents.

CSS backgrounds print by default (`--no-background` disables them). On Windows,
avoid redirecting `-o -` in Windows PowerShell 5.1 (`powershell.exe` re-encodes
native output as text and corrupts the bytes) — let it write the file, or
redirect from cmd.exe / PowerShell 7+.

## Flags

Flags may appear before or after the positional argument. `url-to-pdf`,
`html-to-pdf` and `download` share the output flags; `status` takes only the
connection flags.

| Flag | Applies to | Meaning | Default |
| :-- | :-- | :-- | :-- |
| `-o`, `--output DIR` | url-to-pdf, html-to-pdf, download | output **directory** (created if missing); `-` streams the PDF to stdout | current directory |
| `-f`, `--file-name NAME` | url-to-pdf, html-to-pdf, download | file name inside the output directory (bare name, no path) | `<job-id>.pdf` |
| `--format SIZE` | url-to-pdf, html-to-pdf | page format, case-insensitive: `A0`–`A6`, `Letter`, `Legal`, `Tabloid`, `Ledger` | `A4` |
| `--landscape` | url-to-pdf, html-to-pdf | landscape orientation | portrait |
| `--margin VALUE` | url-to-pdf, html-to-pdf | page margins, each **with a unit** (`mm`, `cm`, `in`, `px`): one value for all sides, or CSS shorthand `top,right,bottom,left` (`--margin 10mm,1cm,0.5in,20px`; 2 values = vertical,horizontal; 3 = top,horizontal,bottom) | renderer default |
| `--margin-top`, `--margin-right`, `--margin-bottom`, `--margin-left VALUE` | url-to-pdf, html-to-pdf | one side, same units; overrides `--margin` for that side | renderer default |
| `--no-background` | url-to-pdf, html-to-pdf | skip CSS backgrounds | backgrounds print |
| `--test` | url-to-pdf, html-to-pdf | free test run: full pipeline, sample PDF, no quota used | off |
| `--timeout DUR` | url-to-pdf, html-to-pdf | how long to wait for rendering (`90s`, `3m`) | `3m` |
| `-q`, `--quiet` | url-to-pdf, html-to-pdf, download | no progress lines; warnings and errors still print | off |
| `--api-key KEY` | all | API key (prefer `PDFIK_API_KEY` — flag values are visible to other processes and shell history) | `$PDFIK_API_KEY` |
| `--api-url URL` | all | API base URL (`https://` only, loopback excepted) | `$PDFIK_API_URL` or `https://api.pdfik.net` |
| `-h`, `--help` | all | usage | |

`pdfik wkhtmltopdf` takes wkhtmltopdf's own flags and positional `<input> <output.pdf>`
instead — see [COMPATIBILITY.md](COMPATIBILITY.md).

## Environment

| Variable | Meaning |
| :-- | :-- |
| `PDFIK_API_KEY` | API key; preferred over `--api-key` |
| `PDFIK_API_URL` | API base URL; the flag wins |
| `PDFIK_INSECURE_HTTP=1` | allow a plain `http://` API URL on a non-loopback host (lab use only; a warning is printed once) |
| `HTTPS_PROXY`, `NO_PROXY` | honoured, as in every Go program |
| `SSL_CERT_FILE` | custom CA bundle on Linux/BSD (Go reads it there only; the container image ships the standard roots) |

## Exit codes

Scripts can branch on these; they are also listed in `pdfik help`.

| Code | Meaning |
| :--: | :-- |
| `0` | PDF written |
| `1` | the request, render or download failed (API, network, file). If `job … queued` was printed, the job may still finish — `pdfik status <id>` / `pdfik download <id>` |
| `2` | invalid usage, or a value refused before anything was submitted (nothing charged) |
| `3` | rendering failed on the server; the error code is printed (see [error codes](https://docs.pdfik.net/error-codes)) |
| `4` | the job was not finished within `--timeout`; the job id is printed — fetch it later with `pdfik download <job-id>` |
| `130` | interrupted (Ctrl-C); any in-flight job keeps running on the server and its id is printed |

## wkhtmltopdf mode

```bash
alias wkhtmltopdf='pdfik wkhtmltopdf'
wkhtmltopdf -s A4 -O Landscape --footer-center 'Page [page] of [topage]' https://example.com out.pdf
wkhtmltopdf --username user --password pass https://intranet/report report.pdf   # Pro+
```

Every wkhtmltopdf flag is either mapped to the API, accepted with a warning
(no effect on this pipeline) or refused with a reason (it would change the
output) — never silently ignored. `wkhtmltopdf --version` and `-h` answer as
wrappers expect. The complete tables are in [COMPATIBILITY.md](COMPATIBILITY.md).

## Behaviour worth knowing

- **Nothing is charged for a bad invocation.** Formats, margin units, output
  paths and flag combinations are validated before the job is submitted.
- **Files are written atomically** (temp file + rename), so a failed download
  never truncates a PDF from an earlier run.
- **Polling respects the API's rate limits** (2 s → 10 s back-off, `Retry-After`
  honoured); a lost submit response is retried under one idempotency key, so a
  job is never created — or charged — twice.
- **The API key never leaves the API host:** it is stripped from cross-host
  redirects, never logged, and plain `http://` is refused unless you opt in.
- Text that comes back from the network is stripped of terminal control
  characters before it is printed.

## Development

```bash
go build ./cmd/pdfik
go test -race ./...          # offline; a fake API is built into the suite
go vet ./... && gofmt -l .
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the ground rules and
[SECURITY.md](SECURITY.md) for reporting vulnerabilities. Releases follow
[semantic versioning](https://semver.org/); changes are listed in
[CHANGELOG.md](CHANGELOG.md). Only the latest release receives fixes.

Docs: [docs.pdfik.net](https://docs.pdfik.net) · API reference:
[api.pdfik.net/docs](https://api.pdfik.net/docs) · Support: support@pdfik.net
