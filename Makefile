GO ?= go
GOFILES := $(shell find . -name '*.go' -not -path './vendor/*' -not -path './output/*')
GOBIN ?= $(shell $(GO) env GOPATH)/bin
GOLANGCI_LINT := $(GOBIN)/golangci-lint
GOVULNCHECK := $(GOBIN)/govulncheck
.DEFAULT_GOAL := ci

.PHONY: fmt fmt-check vet lint build test govulncheck tidy-check ci tools

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "The following files need gofmt:" >&2; \
		echo "$$files" >&2; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

$(GOLANGCI_LINT):
	GO111MODULE=on $(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8

lint: $(GOLANGCI_LINT)
	GO111MODULE=on GOFLAGS=-mod=mod $(GOLANGCI_LINT) run --timeout=5m

build:
	$(GO) build .

test: build
	$(GO) test -race -coverprofile=coverage.out ./...

$(GOVULNCHECK):
	GO111MODULE=on $(GO) install golang.org/x/vuln/cmd/govulncheck@v1.1.4

govulncheck: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

tidy-check:
	$(GO) mod tidy
	@git diff --quiet -- go.mod go.sum || (echo "go.mod/go.sum are not tidy" >&2; exit 1)

tools: $(GOLANGCI_LINT) $(GOVULNCHECK)

ci: fmt-check vet lint test govulncheck tidy-check
