.DEFAULT_GOAL := build
.PHONY: fmt vet verify build test test-terminal test-integration test-reference test-desktop vuln release release-all verify-repro
export GOCACHE := $(CURDIR)/.gocache
export GOMODCACHE := $(CURDIR)/.gomodcache
export GOPATH := $(CURDIR)/.cache/go
export GOTOOLCHAIN := go1.27.1
export CGO_ENABLED := 0
GOFLAGS := -mod=vendor
VERSION ?= dev
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)
LDFLAGS := -s -w -buildid= -X github.com/jenic/passgage/internal/build.Version=$(VERSION)

fmt:
	gofmt -w cmd internal tests
verify:
	go mod verify
vet: verify
	go vet $(GOFLAGS) ./...
build: fmt vet
	go build $(GOFLAGS) -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o bin/passgage$(EXE) ./cmd/passgage
test: fmt vet
	go test $(GOFLAGS) $(TEST_FLAGS) ./...
test-integration: verify
	go test $(GOFLAGS) -tags=integration ./tests/...
test-reference: verify
	go test $(GOFLAGS) -tags=reference ./tests/e2e
test-desktop: build
	PASSGAGE_BINARY="$(CURDIR)/bin/passgage$(EXE)" go test $(GOFLAGS) -tags=desktop ./tests/smoke
test-terminal: build
	PASSGAGE_BINARY="$(CURDIR)/bin/passgage$(EXE)" go test $(GOFLAGS) -tags=terminal -timeout=120s ./tests/terminal
vuln: verify
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./cmd/passgage
release: release-all
release-all: verify
	go run $(GOFLAGS) ./internal/release -version '$(VERSION)'
verify-repro: verify
	go run $(GOFLAGS) ./internal/release -version '$(VERSION)' -verify
