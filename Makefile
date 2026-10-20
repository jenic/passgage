.DEFAULT_GOAL := build
.PHONY: fmt vet verify build install test test-completion test-release test-terminal test-integration test-reference test-desktop vuln release release-all verify-repro pin-actions publish-check publish
export GOCACHE := $(CURDIR)/.gocache
export GOMODCACHE := $(CURDIR)/.gomodcache
export GOPATH := $(CURDIR)/.cache/go
export GOTOOLCHAIN := go1.27.1
export CGO_ENABLED := 0
GOFLAGS := -mod=vendor
VERSION ?= dev
export PUBLISH ?= 0
EXE := $(if $(filter Windows_NT,$(OS)),.exe,)
ifeq ($(OS),Windows_NT)
BINDIR ?= $(subst \,/,$(LOCALAPPDATA))/Programs/passgage
else
BINDIR ?= /usr/local/bin
endif
LDFLAGS := -s -w -buildid= -X github.com/jenic/passgage/internal/build.Version=$(VERSION)

fmt:
	gofmt -w cmd internal tests
verify:
	go mod verify
vet: verify
	go vet $(GOFLAGS) ./...
build: fmt vet
	go build $(GOFLAGS) -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o bin/passgage$(EXE) ./cmd/passgage
install: build
ifeq ($(OS),Windows_NT)
	@test -n "$(LOCALAPPDATA)" || { echo "LOCALAPPDATA must be set for Windows installation" >&2; exit 1; }
	install -d "$(BINDIR)"
	install -m 0755 "bin/passgage$(EXE)" "$(BINDIR)/passgage$(EXE)"
else
	@set -e; \
	install_dir="$(BINDIR)"; \
	while [ ! -e "$$install_dir" ]; do install_dir=$$(dirname "$$install_dir"); done; \
	elevate=; \
	if [ ! -w "$$install_dir" ] || { [ -e "$(BINDIR)/passgage" ] && [ ! -w "$(BINDIR)/passgage" ]; }; then \
		elevate=sudo; \
	fi; \
	$$elevate install -d "$(BINDIR)"; \
	$$elevate install -m 0755 bin/passgage "$(BINDIR)/passgage"
endif
test: fmt vet
	go test $(GOFLAGS) $(TEST_FLAGS) ./...
test-completion: build
	PASSGAGE_BINARY="$(CURDIR)/bin/passgage$(EXE)" go test $(GOFLAGS) -tags=completion -timeout=180s ./tests/completion
test-release:
	python3 -B -m unittest discover -s .github/scripts -p 'test_*.py'
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
publish-check publish: export RELEASE_TAG = $(VERSION)
publish-check:
	@bash .github/scripts/publish.sh check
publish: publish-check
	$(MAKE) test test-release
	@bash .github/scripts/publish.sh publish
pin-actions:
	pinact run --update --verify-comment
