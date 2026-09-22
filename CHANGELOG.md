# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [0.3.0] — 2026-09-19

### Fixed
- A per-minute request limit (`RATE_LIMIT_EXCEEDED`) or the in-flight job cap
  (`CONCURRENCY_LIMIT_EXCEEDED`) no longer fails a command: submitting (same idempotency
  key), `pdfik status` and `pdfik download` wait out `Retry-After` (capped at 60 s, at most
  five waits) and carry on. Found by the client check against staging (E2E step 8.10.6).
  Quota 429s (monthly PDFs or bytes) are still reported at once — waiting does not clear
  them. Polling during a render already waited out 429s.
- README and docs: the "pages behind a login" example pointed at `https://intranet/…`, a
  host a cloud renderer can never reach; the sample URLs `example.com/report` and
  `example.com/invoice/42` now answer 404, so copied examples failed. They use
  `https://example.com`.

### Added
- `markdown-to-pdf <file|->`: convert Markdown (CommonMark + GFM tables and
  strikethrough; raw HTML is escaped by the API) to PDF with the same page
  flags as `html-to-pdf` (`--format`, `--landscape`, `--margin*`,
  `--no-background`) plus `-o`/`-f`/`--test`/`--timeout`/`-q`.
- `url-to-image <url>` and `html-to-image <file|->`: PNG/JPEG screenshots.
  Flags: `--format png|jpeg` (`jpg` accepted as an alias), `--full-page`
  (capture the whole scrollable page, clipped at 8192 px; the default is the
  visible area only), `--quality N` (1–100, jpeg only — refused locally for
  png), `--viewport WxH` — the window the page opens in, width 320–1920 and
  height 320–8192 CSS px, e.g. `1024x768` (the default). Output defaults to `<job-id>.png`/`.jpg`
  by chosen format; `--test` returns the API's bundled sample image.
- `--deliver-url <presigned PUT URL>` on `url-to-pdf`, `html-to-pdf`,
  `markdown-to-pdf`, `url-to-image` and `html-to-image` (Pro+ plans): the
  output is uploaded straight to your own bucket and nothing is stored on
  PDFik's side, so the CLI skips the download and prints
  `job <id> delivered: <url-without-query>` — taken from the flag you passed,
  not from the API, which never records the destination. Combining with `-o`/`-f` is a
  usage error (exit 2); combining with `--test` surfaces the API's 400.
- The `Accept` header now also lists `image/png, image/jpeg`; `-o` refuses a
  directory argument ending in `.png`/`.jpg`/`.jpeg` the same way it refuses
  `.pdf`.

### Changed
- `pdfik download <job-id>` without `-f` names the file after the kind of
  output the job produced, taken from the download's `Content-Type`:
  `<job-id>.pdf`, `.png` or `.jpg`. A screenshot job fetched later — the
  path every timeout and interruption hint points at — no longer lands as a
  `.pdf` with PNG/JPEG bytes inside. `-f NAME` is kept exactly as given, and
  `-o -` is unaffected. A failed final rename now reports "The output was
  kept at …" instead of "The PDF was kept at …".
- `--deliver-url` runs that time out (exit 4), are interrupted (exit 130) or
  give up after repeated status-check failures (exit 1) no longer advise
  `pdfik download <id>` — the API
  answers 404 `output-delivered-externally` for such jobs. The hint says to
  check the job with `pdfik status <id>` and that the output goes to the
  delivered URL (query string stripped).

## [0.2.0] — 2026-09-06

### Added
- `einvoice-to-pdf`: build a Factur-X (PDF/A-3) hybrid e-invoice from
  UN/CEFACT CII XML (file or stdin), with `--profile` (minimum, basicwl,
  basic, en16931, extended), `--template` for a saved invoice template and
  `--webhook`; shares the output flags, `--test`, `--timeout` and `-q` with
  the other commands. Profiles minimum/basicwl print a note that the result
  is not a legally sufficient e-invoice.

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

[0.3.0]: https://github.com/pdfik/cli/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/pdfik/cli/compare/v0.1.1...v0.2.0
[0.1.0]: https://github.com/pdfik/cli/releases/tag/v0.1.0
