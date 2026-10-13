SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

export CGO_ENABLED := 0

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE ?= ghcr.io/bothyhq/bothy:dev
LDFLAGS := -s -w -X main.version=$(VERSION)

# Go-installed tools are built with the repository toolchain; linters built
# with an older Go refuse to analyse go1.27 code.
GO_TOOLCHAIN := $(shell awk '/^toolchain /{print $$2}' go.mod)
TOOLS_BIN := bin/tools
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION := v1.8.0

.PHONY: all build test ui ui-reset gen lint vuln e2e image budgets clean

all: build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/bothy ./cmd/bothy

# The race detector needs cgo on Linux; only test binaries are built with it.
# disgo v0.19.6's gateway races internally, so the Discord tests that drive
# the real gateway are built only without -race; the second run covers them.
test:
	CGO_ENABLED=1 go test -race ./...
	go test -count=1 ./internal/channel/discord/...

ui:
	cd web && npm ci --ignore-scripts && npm run build

# Puts the committed placeholder back so a UI build is never committed.
ui-reset:
	find internal/webui/dist -mindepth 1 ! -name index.html -delete
	git checkout -- internal/webui/dist/index.html

gen:
	go -C tools run ./typesgen -root ..

$(TOOLS_BIN)/golangci-lint:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN="$(CURDIR)/$(TOOLS_BIN)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(TOOLS_BIN)/govulncheck:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN="$(CURDIR)/$(TOOLS_BIN)" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# staticcheck runs as a golangci-lint linter: the newest standalone release
# outside the 14-day cooldown (v0.8.1) cannot read go1.27 export data.
lint: $(TOOLS_BIN)/golangci-lint
	@test -z "$$(gofmt -l cmd internal e2e tools)" || { gofmt -l cmd internal e2e tools; echo "gofmt: files above need formatting"; exit 1; }
	go vet ./...
	go vet -tags 'e2e budgets' ./e2e/...
	go -C tools vet ./...
	$(TOOLS_BIN)/golangci-lint run ./...
	cd tools && ../$(TOOLS_BIN)/golangci-lint run ./...
	scripts/check-imports.sh
	scripts/check-ownership.sh

vuln: $(TOOLS_BIN)/govulncheck
	$(TOOLS_BIN)/govulncheck ./...

e2e:
	go test -tags e2e -count=1 ./e2e/...

image:
	docker buildx build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t $(IMAGE) --load .

budgets:
	IMAGE=$(IMAGE) scripts/budgets/run.sh

clean:
	rm -rf bin
