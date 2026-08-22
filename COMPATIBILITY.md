# wkhtmltopdf mode

`pdfik wkhtmltopdf [flags] <input> <output>` is a translator for the common
wkhtmltopdf invocations, not an emulator. Every wkhtmltopdf flag falls into one
of three classes — **mapped** (same meaning), **accepted with a warning** (no
effect on this pipeline) or **refused with a reason** (it would change the
output). Nothing is silently ignored; an unknown flag stops the run before
anything is submitted, so a bad invocation never costs a render.

`<input>` is a URL (`https://…`), a local HTML file, `file://…` or `-` (stdin).
`<output>` is the PDF path, an existing directory (the file is named
`<job-id>.pdf` inside it) or `-` (stdout); a missing output directory is created.

## Mapped flags

| Flag | Notes |
| :-- | :-- |
| `-s`, `--page-size` | `A4` (default), `A0`–`A6`, `Letter`, `Legal`, `Tabloid`, `Ledger` |
| `-O`, `--orientation` | `Portrait` / `Landscape` |
| `-T`, `--margin-top` | bare numbers mean mm, as in wkhtmltopdf; `pt` is converted; default 10mm |
| `-B`, `--margin-bottom` | same |
| `-L`, `--margin-left` | same |
| `-R`, `--margin-right` | same |
| `--background`, `--no-background` | backgrounds print by default, as in wkhtmltopdf |
| `--zoom` | 0.5–2.0 |
| `--disable-smart-shrinking` | pins scale to 1:1; auto-fit is on by default |
| `--header-left`, `--header-center`, `--header-right` | text with `[page]`, `[topage]`, `[sitepage]`, `[sitepages]`, `[frompage]`, `[date]`, `[isodate]`, `[time]`, `[title]`, `[doctitle]`, `[webpage]` (case-insensitive; `[isodate]`/`[time]` are resolved at submission time; `[webpage]` is the file:// path for local input) |
| `--footer-left`, `--footer-center`, `--footer-right` | same |
| `--header-font-size`, `--footer-font-size` | points |
| `--header-font-name`, `--footer-font-name` | letters, digits, spaces and hyphens (`Noto Sans CJK JP`) |
| `--header-line`, `--footer-line`, `--no-header-line`, `--no-footer-line` | |
| `--header-html`, `--footer-html` | local file, static markup with the same `[page]`-style tokens |
| `--default-header` | `[webpage]` left, `[page]/[topage]` right, with a line |
| `--replace` | `--replace name value` substitutes `[name]` in headers and footers |
| `--title` | feeds `[title]`/`[doctitle]`; the PDF metadata title is not set (warned) |
| `--javascript-delay` | 0–10000 ms · Pro+ |
| `--username`, `--password` | basic auth for URL input · Pro+ |
| `--image-quality` | 1–100 · Pro+ |
| `--encoding` | UTF-8 spellings accepted; anything else is refused (re-encode the file first) |
| `--load-error-handling`, `--load-media-error-handling` | `ignore`/`skip` is this pipeline's behaviour (failed subresources render missing; a main document that fails still fails the job); `abort` is refused |
| `-q`, `--quiet` | no progress lines; warnings still print |

"Pro+" means the option needs the Pro plan or higher; on other plans the API
answers 402 before anything is rendered.

## Accepted with a warning

No effect on the output this pipeline produces. The warning names the flag and
the reason.

| Flag | Reason |
| :-- | :-- |
| `--print-media-type` | print media CSS is always used |
| `--enable-javascript` | JavaScript is on for URL input |
| `--enable-smart-shrinking` | auto-fit is the default |
| `--stop-slow-scripts`, `--no-stop-slow-scripts` | the rendering timeout is the watchdog |
| `--enable-local-file-access`, `--disable-local-file-access` | the HTML file itself is read locally; relative assets are not resolved |
| `-d`, `--dpi` | PDF output is vector |
| `--image-dpi` | image DPI is the source page's |
| `-l`, `--lowquality` | use `--image-quality` instead |
| `--header-spacing`, `--footer-spacing` | header/footer gap is fixed |
| `--copies` | printing-only |
| `--collate`, `--no-collate` | printing-only |
| `--log-level` | verbosity is `--quiet` |
| `--no-outline` | outlines are never generated |
| `--dump-outline` | outlines are never generated |
| `--disable-external-links`, `--enable-external-links`, `--disable-internal-links`, `--enable-internal-links` | link annotations follow Chromium |
| `--disable-forms` | forms are never generated |
| `--images` | images load by default |
| `--keep-relative-links`, `--resolve-relative-links` | link annotations follow Chromium |
| `--debug-javascript`, `--no-debug-javascript` | script diagnostics are not reported |
| `--disable-plugins`, `--enable-plugins` | no browser plugins in the renderer |
| `--exclude-from-outline`, `--include-in-outline`, `--disable-toc-back-links`, `--enable-toc-back-links` | no outlines or tables of contents |
| `--no-pdf-compression` | stream compression is the renderer's |
| `--use-xserver` | rendering happens in the cloud |
| `--custom-header-propagation`, `--no-custom-header-propagation` | custom headers are not supported |
| `--proxy-hostname-lookup`, `--bypass-proxy-for` | proxies are not supported |
| `--allow` | local access rules do not apply |
| `--cache-dir` | no local cache |
| `--checkbox-svg`, `--checkbox-checked-svg`, `--radiobutton-svg`, `--radiobutton-checked-svg` | form controls follow Chromium |

## Refused

These would change the rendered output, so the run stops with the reason
before anything is submitted.

| Flag | Reason |
| :-- | :-- |
| `-g`, `--grayscale` | no grayscale conversion |
| `-n`, `--disable-javascript` | JavaScript cannot be turned off for URL input |
| `--no-print-media-type` | screen-media rendering is not supported |
| `--window-status` | waiting on window.status is not supported |
| `--cookie` | per-request cookies are not supported |
| `--cookie-jar` | cookies are not supported |
| `--custom-header` | custom request headers are not supported |
| `--page-width`, `--page-height` | use `--page-size` |
| `--page-offset` | page numbers always start at 1 |
| `--read-args-from-stdin` | batch mode is not supported yet |
| `toc` | tables of contents are not supported |
| `cover` | cover pages are not supported |
| `--outline`, `--outline-depth` | outlines are not generated |
| `--dump-default-toc-xsl`, `--xsl-style-sheet` | tables of contents are not supported |
| `--user-style-sheet` | injecting a stylesheet is not supported |
| `--run-script` | injecting scripts is not supported |
| `--viewport-size` | a custom viewport is not supported |
| `--minimum-font-size` | not supported |
| `-p`, `--proxy` | proxying the fetch is not supported |
| `--post`, `--post-file` | POST navigation is not supported |
| `--enable-forms` | interactive forms are not supported |
| `--no-images` | suppressing images is not supported |
| `--ssl-crt-path`, `--ssl-key-path`, `--ssl-key-password` | client certificates are not supported |
| `--readme`, `--htmldoc`, `--manpage`, `--license` | documentation lives in README.md, COMPATIBILITY.md and LICENSE |

## Extensions and probes

`--test` (free run, returns a sample PDF) · `--api-key` · `--api-url` ·
`--timeout` · `-V`/`--version` and `-h`/`--help` answer anywhere in the
argument list, as wkhtmltopdf wrappers expect.

## Good to know

- Rendering uses print CSS and headless Chromium; PDF outlines are not generated.
- URL input renders with full fidelity. Local HTML is sanitized by the API
  (styles/scripts stripped, relative assets unresolved) — prefer URLs for styled
  documents.
- Header/footer templates may use only basic markup (span/div/p/b/i/strong/em/
  small/br and safe inline styles); the API caps them at 10 000 characters.
- Exit codes are the same as for the native commands — see `pdfik help`.

Missing something you need? Open an issue.
