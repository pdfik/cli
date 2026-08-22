# Security policy

## Reporting a vulnerability

Email **security@pdfik.net** (published in [security.txt](https://pdfik.net/.well-known/security.txt)) with a description, reproduction steps and the
version (`pdfik version`). Please do not open a public issue for anything that
could expose API keys or rendered documents. You will get an acknowledgement
within 3 business days and a fix or mitigation plan within 14 days for confirmed
issues.

## Scope

This repository contains the command-line client only. Issues in the PDFik API
or the rendering service are in scope too — report them to the same address.

## What the CLI does to protect you

- The API key is read from `PDFIK_API_KEY` (preferred) or `--api-key`; it is
  never written to logs or error messages, and it is stripped from any redirect
  that leaves the API host.
- Plain `http://` API URLs are refused (loopback excepted) unless you set
  `PDFIK_INSECURE_HTTP=1` deliberately.
- Text that comes back from the network is stripped of terminal control
  characters before it is printed.
- Files are written atomically (temp file + rename), so a failed download never
  truncates a PDF from an earlier run.

## Supported versions

Only the latest release receives security fixes.
