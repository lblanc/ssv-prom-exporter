BINARY := ssv-prom-exporter
PKG    := ./cmd/$(BINARY)
BIN    := bin

.PHONY: build build-windows run-ping clean tidy vet

build:
	go build -o $(BIN)/$(BINARY) $(PKG)

build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o $(BIN)/$(BINARY).exe $(PKG)

run-ping: build
	./$(BIN)/$(BINARY) -ping

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)/
