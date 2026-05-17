# syntax=docker/dockerfile:1.7

# ------------------------------------------------------------------
# Stage 1 — build the static linux/amd64 binary.
# ------------------------------------------------------------------
FROM golang:1.26-alpine AS builder

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# Cache the module download separately so source edits don't bust it.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/ssv-prom-exporter \
      ./cmd/ssv-prom-exporter

# ------------------------------------------------------------------
# Stage 2 — minimal runtime.
# Alpine is preferred over distroless here because we want
# `wget` for HEALTHCHECK and a real /bin/sh for one-off debugging.
# ------------------------------------------------------------------
FROM alpine:3.20

ARG VERSION=dev

# Standard OCI labels — the GitHub Container Registry surfaces them in
# the package UI; tooling like Renovate / Dependabot reads them too.
LABEL org.opencontainers.image.title="ssv-prom-exporter" \
      org.opencontainers.image.description="Prometheus exporter for DataCore SANsymphony" \
      org.opencontainers.image.source="https://github.com/lblanc/ssv-prom-exporter" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache ca-certificates tini wget \
 && addgroup -S -g 65532 ssv \
 && adduser  -S -G ssv -u 65532 -h /nonexistent -s /sbin/nologin ssv

COPY --from=builder /out/ssv-prom-exporter /usr/local/bin/ssv-prom-exporter
COPY config.example.yaml /etc/ssv-prom-exporter/config.example.yaml
COPY LICENSE /licenses/LICENSE

USER 65532:65532
EXPOSE 9876

# /metrics returning 200 is the exporter's authoritative liveness signal.
# 30s start period lets the first inventory scrape complete before the
# container is judged unhealthy.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD wget --quiet --tries=1 --spider \
        "http://127.0.0.1:9876/metrics" || exit 1

# Use tini so SIGTERM from `docker stop` is forwarded to the Go binary
# (which already handles graceful shutdown via os/signal).
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/ssv-prom-exporter"]
CMD ["-listen", ":9876"]
