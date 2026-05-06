BINARY  := ssv-prom-exporter
PKG     := ./cmd/$(BINARY)
BIN     := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# MSI ProductVersion only accepts X.Y[.Z[.W]] with X<=255. Strip a leading
# 'v' and any '-...' suffix; fall back to 0.0.0 for non-tag builds.
MSI_VERSION := $(shell echo "$(VERSION)" | sed -E 's/^v//; s/-.*//' | grep -E '^[0-9]+(\.[0-9]+){0,3}$$' || echo 0.0.0)
MSI     := $(BIN)/$(BINARY)-$(MSI_VERSION)-x64.msi

.PHONY: build build-windows run-ping clean tidy vet test msi

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN)/$(BINARY) $(PKG)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	  go build -ldflags "-X main.version=$(VERSION)" -o $(BIN)/$(BINARY).exe $(PKG)

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

clean:
	rm -rf $(BIN)/
