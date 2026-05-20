BINARY  := ssv-prom-exporter
PKG     := ./cmd/$(BINARY)
BIN     := bin
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# MSI ProductVersion only accepts X.Y[.Z[.W]] with X<=255. Strip a leading
# 'v' and any '-...' suffix; fall back to 0.0.0 for non-tag builds.
MSI_VERSION := $(shell echo "$(VERSION)" | sed -E 's/^v//; s/-.*//' | grep -E '^[0-9]+(\.[0-9]+){0,3}$$' || echo 0.0.0)
MSI     := $(BIN)/$(BINARY)-$(MSI_VERSION)-x64.msi

# Docker image coordinates. Override IMAGE_REGISTRY / IMAGE_NAME to push
# elsewhere than GHCR (e.g. a private registry).
IMAGE_REGISTRY ?= ghcr.io
IMAGE_NAME     ?= lblanc/$(BINARY)
IMAGE          := $(IMAGE_REGISTRY)/$(IMAGE_NAME)
IMAGE_TAG      ?= $(VERSION)

.PHONY: build build-linux build-windows run-ping clean tidy vet test msi \
        tarball-linux docker-build docker-push \
        build-prom-clip build-prom-clip-linux run-prom-clip

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN)/$(BINARY) $(PKG)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	  go build -ldflags "-X main.version=$(VERSION)" -o $(BIN)/$(BINARY).exe $(PKG)

# Static linux/amd64 binary, suitable for tarball + Docker.
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
	  -o $(BIN)/$(BINARY)-linux-amd64 $(PKG)

# prom-clip: small web tool that exports a Prometheus time-window to
# OpenMetrics (.txt.gz) and replays it into another Prometheus via
# remote write. Independent of SSV; lives in this repo for convenience.
build-prom-clip:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN)/prom-clip ./cmd/prom-clip

build-prom-clip-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
	  -o $(BIN)/prom-clip-linux-amd64 ./cmd/prom-clip

run-prom-clip: build-prom-clip
	./$(BIN)/prom-clip -listen :8088

run-ping: build
	./$(BIN)/$(BINARY) -ping

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy

# Build the MSI from a fresh windows binary. Requires `wixl` (Debian
# package) on the build host. The MSI's ProductVersion is derived from
# the git tag (`vX.Y.Z`); see MSI_VERSION above.
msi: build-windows
	wixl -a x64 -D Version=$(MSI_VERSION) -o $(MSI) packaging/windows/installer.wxs
	@echo "built $(MSI)"

# Linux tarball: static binary + systemd unit + example config + LICENSE
# + README, suitable for `tar xzf` + `cp` + `systemctl enable`.
tarball-linux: build-linux
	@mkdir -p $(DIST)
	@tmp=$$(mktemp -d) ; \
	  pkg="$$tmp/$(BINARY)-$(VERSION)-linux-amd64" ; \
	  mkdir -p "$$pkg" ; \
	  cp $(BIN)/$(BINARY)-linux-amd64        "$$pkg/$(BINARY)" ; \
	  cp config.example.yaml                  "$$pkg/" ; \
	  cp packaging/linux/ssv-prom-exporter.service "$$pkg/" ; \
	  cp packaging/linux/install-linux.sh     "$$pkg/" 2>/dev/null || true ; \
	  cp LICENSE                              "$$pkg/LICENSE" ; \
	  cp README.md                            "$$pkg/README.md" 2>/dev/null || true ; \
	  tar -C "$$tmp" -czf "$(DIST)/$(BINARY)-$(VERSION)-linux-amd64.tar.gz" \
	      "$(BINARY)-$(VERSION)-linux-amd64" ; \
	  rm -rf "$$tmp" ; \
	  echo "built $(DIST)/$(BINARY)-$(VERSION)-linux-amd64.tar.gz"

# Docker image — single linux/amd64 layer, ~25 MB. For multi-arch use
# `docker buildx build --platform linux/amd64,linux/arm64 ...` directly.
docker-build:
	docker build \
	  --build-arg VERSION=$(VERSION) \
	  --tag $(IMAGE):$(IMAGE_TAG) \
	  --tag $(IMAGE):latest \
	  .

docker-push: docker-build
	docker push $(IMAGE):$(IMAGE_TAG)
	docker push $(IMAGE):latest

clean:
	rm -rf $(BIN)/ $(DIST)/
