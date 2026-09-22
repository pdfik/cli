# pdfik CLI

> **Where this code lives:** developed in the PDFik platform monorepo; this tree is its commit `b3b9cc7`.
> Issues and PRs are welcome here; accepted PRs are applied upstream and land with the next sync (see CONTRIBUTING.md).

[![CI](https://github.com/pdfik/cli/actions/workflows/ci.yml/badge.svg)](https://github.com/pdfik/cli/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/pdfik/cli.svg)](https://pkg.go.dev/github.com/pdfik/cli)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

wkhtmltopdf-compatible command-line client for [PDFik](https://pdfik.net) —
render URLs, HTML and Markdown to PDF, or capture URLs and HTML as PNG/JPEG
screenshots, through the cloud API. One static binary, standard library only,
no dependencies; also a few-megabyte container image.

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
- **Docker:** `docker pull ghcr.io/pdfik/cli:0.2.0` — linux/amd64 and linux/arm64;
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
pdfik markdown-to-pdf README.md -f readme.pdf                   # Markdown (GFM tables) → PDF
pdfik url-to-image https://example.com --format jpeg --quality 80   # screenshot → ./<job-id>.jpg
pdfik url-to-pdf https://example.com --deliver-url "$PRESIGNED_PUT_URL"  # straight to your bucket (Pro+)
pdfik einvoice-to-pdf invoice.xml --profile en16931             # Factur-X e-invoice (PDF/A-3)
pdfik status   <job-id>
pdfik download <job-id> -o ./pdfs                               # fetch a finished job later (.pdf/.png/.jpg)
```

**`html-to-pdf` / `html-to-image` input is sanitized by the API** (`<style>`,
`<script>` and `class` attributes are stripped server-side; relative assets are
not resolved). **`url-to-pdf` / `url-to-image` render with full fidelity** —
prefer them for styled pages. `markdown-to-pdf` renders CommonMark plus GFM
tables/strikethrough with a built-in print stylesheet; raw HTML inside the
Markdown is escaped, not rendered.

CSS backgrounds print by default (`--no-background` disables them). On Windows,
avoid redirecting `-o -` in Windows PowerShell 5.1 (`powershell.exe` re-encodes
native output as text and corrupts the bytes) — let it write the file, or
redirect from cmd.exe / PowerShell 7+.

## Flags

Flags may appear before or after the positional argument. *The rendering
commands* below are `url-to-pdf`, `html-to-pdf`, `markdown-to-pdf`,
`url-to-image`, `html-to-image` and `einvoice-to-pdf`; they and `download`
share the output flags, `status` takes only the connection flags. The
page-layout flags apply to the three PDF page renderers (`url-to-pdf`,
`html-to-pdf`, `markdown-to-pdf`), the screenshot flags to `url-to-image` and
`html-to-image` — an e-invoice's layout comes from its template (see
[E-invoicing](#e-invoicing-factur-x)).

| Flag | Applies to | Meaning | Default |
| :-- | :-- | :-- | :-- |
| `-o`, `--output DIR` | rendering commands, download | output **directory** (created if missing); `-` streams the output to stdout | current directory |
| `-f`, `--file-name NAME` | rendering commands, download | file name inside the output directory (bare name, no path) | `<job-id>.pdf` (`.png` / `.jpg` for the image commands; `download` picks the extension from the kind of output the job produced) |
| `--format SIZE` | url-to-pdf, html-to-pdf, markdown-to-pdf | page format, case-insensitive: `A0`–`A6`, `Letter`, `Legal`, `Tabloid`, `Ledger` | `A4` |
| `--landscape` | url-to-pdf, html-to-pdf, markdown-to-pdf | landscape orientation | portrait |
| `--margin VALUE` | url-to-pdf, html-to-pdf, markdown-to-pdf | page margins, each **with a unit** (`mm`, `cm`, `in`, `px`): one value for all sides, or CSS shorthand `top,right,bottom,left` (`--margin 10mm,1cm,0.5in,20px`; 2 values = vertical,horizontal; 3 = top,horizontal,bottom) | renderer default |
| `--margin-top`, `--margin-right`, `--margin-bottom`, `--margin-left VALUE` | url-to-pdf, html-to-pdf, markdown-to-pdf | one side, same units; overrides `--margin` for that side | renderer default |
| `--no-background` | url-to-pdf, html-to-pdf, markdown-to-pdf | skip CSS backgrounds | backgrounds print |
| `--format png\|jpeg` | url-to-image, html-to-image | image format (`jpg` is accepted as an alias for `jpeg`) | `png` |
| `--full-page` | url-to-image, html-to-image | capture the whole scrollable page instead of just the visible area; the height follows the real page and is clipped at 8192 px (a ceiling, not a target — a 2,000 px page gives a 2,000 px image) | visible area only |
| `--quality N` | url-to-image, html-to-image | JPEG quality `1`–`100` (jpeg only) | renderer default |
| `--viewport WxH` | url-to-image, html-to-image | the browser window the page opens in, in CSS pixels (`--viewport 1024x768`) — you pick the size and the API captures exactly that, nothing is scaled or fitted; width `320`–`1920`, height `320`–`8192`. Sets the image width, and without `--full-page` also its height | `1024x768` |
| `--deliver-url URL` | url-to-pdf, html-to-pdf, markdown-to-pdf, url-to-image, html-to-image | upload the output straight to your own storage via this presigned PUT `https` URL (Pro+). Nothing is stored on PDFik's side and nothing is downloaded — the CLI prints the destination you passed (query string stripped) instead of saving a file; the API itself never records it, so the `job.finished` webhook carries no address; not combinable with `-o`/`-f`. If the wait times out or is interrupted, the hint points at `pdfik status <job-id>` and the delivered URL — there is nothing to `pdfik download` | none |
| `--profile NAME` | einvoice-to-pdf | Factur-X conformance profile the XML declares: `minimum`, `basicwl`, `basic`, `en16931`, `extended` | `en16931` |
| `--template ID` | einvoice-to-pdf | id of a saved invoice template (Dashboard → E-Invoice) | the account default template |
| `--webhook URL` | einvoice-to-pdf | callback URL that receives a POST with the job outcome | none |
| `--test` | rendering commands | free test run: full pipeline, sample output, no quota used | off |
| `--timeout DUR` | rendering commands | how long to wait for rendering (`90s`, `3m`) | `3m` |
| `-q`, `--quiet` | rendering commands, download | no progress lines; warnings and errors still print | off |
| `--api-key KEY` | all | API key (prefer `PDFIK_API_KEY` — flag values are visible to other processes and shell history) | `$PDFIK_API_KEY` |
| `--api-url URL` | all | API base URL (`https://` only, loopback excepted) | `$PDFIK_API_URL` or `https://api.pdfik.net` |
| `-h`, `--help` | all | usage | |

`pdfik wkhtmltopdf` takes wkhtmltopdf's own flags and positional `<input> <output.pdf>`
instead — see [COMPATIBILITY.md](COMPATIBILITY.md).

## E-invoicing (Factur-X)

```bash
pdfik einvoice-to-pdf invoice.xml --profile en16931 -f invoice.pdf
pdfik einvoice-to-pdf invoice.xml --template 3e1c…d42 --webhook https://example.com/hooks/pdf
```

`einvoice-to-pdf` takes UN/CEFACT Cross-Industry-Invoice XML (UTF-8, up to
1 MB; `-` reads stdin), builds the human-readable invoice from a block
template — a saved one via `--template`, otherwise the account default — and
returns a PDF/A-3 file with the XML embedded as `factur-x.xml` (a Factur-X /
ZUGFeRD hybrid e-invoice). Invoices (TypeCode 380) and credit notes (381) are
supported. The XML is validated against the official XSD of the declared
`--profile` before anything is charged; the output is validated with veraPDF
and Mustangproject. Schema-valid does not mean tax-compliant — the invoice
content remains your responsibility.

Available on every plan, Free included — the output is the same clean PDF/A-3.
Profiles `minimum` and `basicwl` embed accompanying data only and are **not**
a legally sufficient e-invoice — use `basic`, `en16931` or `extended` for a
full invoice. Templates are created in the dashboard's E-Invoice constructor
(logo, blocks, live preview).

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
| `1` | the request, render or download failed (API, network, file). If `job … queued` was printed, the job may still finish — `pdfik status <id>` / `pdfik download <id>` (`--deliver-url` runs: `pdfik status <id>` only — the output goes to your URL) |
| `2` | invalid usage, or a value refused before anything was submitted (nothing charged) |
| `3` | rendering failed on the server; the error code is printed (see [error codes](https://docs.pdfik.net/error-codes)) |
| `4` | the job was not finished within `--timeout`; the job id is printed — fetch it later with `pdfik download <job-id>` (named `<job-id>.pdf`, `.png` or `.jpg` after the job's output); for a `--deliver-url` run check it with `pdfik status <job-id>` — the output goes to your URL |
| `130` | interrupted (Ctrl-C); any in-flight job keeps running on the server and its id is printed |

## wkhtmltopdf mode

```bash
alias wkhtmltopdf='pdfik wkhtmltopdf'
wkhtmltopdf -s A4 -O Landscape --footer-center 'Page [page] of [topage]' https://example.com out.pdf
wkhtmltopdf --username user --password pass https://example.com report.pdf   # Pro+
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
