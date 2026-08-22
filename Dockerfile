# Container build: the final image is FROM scratch — the static binary plus CA
# roots, a few megabytes, running as a non-root user.
#
#   docker build -t pdfik .
#   docker run --rm --user "$(id -u):$(id -g)" -e PDFIK_API_KEY \
#     -v "$PWD:/work" -w /work pdfik url-to-pdf https://example.com -f example.pdf
#
# --user matters on Linux hosts: the image runs as uid 65532 by default, so
# without it the output PDF would be owned by that uid. (Alternatively `-o -`
# streams to stdout and sidesteps file ownership entirely.)
#
# Multi-arch: the build stage runs on the BUILD platform and cross-compiles for
# the TARGET platform (CGO is off), so `docker buildx build --platform
# linux/amd64,linux/arm64` never compiles Go under QEMU. A plain `docker build`
# still works unchanged — TARGETOS/TARGETARCH default to the native platform.
#
# The toolchain image is pinned by digest; Dependabot bumps it.

FROM --platform=$BUILDPLATFORM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /pdfik ./cmd/pdfik

FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="pdfik" \
      org.opencontainers.image.description="Command-line client for the PDFik PDF generation API (wkhtmltopdf-compatible mode included)" \
      org.opencontainers.image.source="https://github.com/pdfik/cli" \
      org.opencontainers.image.url="https://pdfik.net" \
      org.opencontainers.image.documentation="https://docs.pdfik.net" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /pdfik /pdfik
USER 65532:65532
ENTRYPOINT ["/pdfik"]
CMD ["help"]
