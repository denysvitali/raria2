GO ?= go
GOFILES := $(shell find . -name '*.go' -not -path './vendor/*' -not -path './output/*')
GOBIN ?= $(shell $(GO) env GOPATH)/bin
GOLANGCI_LINT := $(GOBIN)/golangci-lint
GOVULNCHECK := $(GOBIN)/govulncheck
STATICCHECK := $(GOBIN)/staticcheck
GOSEC := $(GOBIN)/gosec
.DEFAULT_GOAL := ci

.PHONY: fmt fmt-check vet lint lint-extra security build test govulncheck staticcheck gosec tidy-check ci tools

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
	GO111MODULE=on $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.8.0

lint: $(GOLANGCI_LINT)
	GO111MODULE=on GOFLAGS=-mod=mod $(GOLANGCI_LINT) run --timeout=5m

$(STATICCHECK):
	GO111MODULE=on $(GO) install honnef.co/go/tools/cmd/staticcheck@latest

staticcheck: $(STATICCHECK)
	$(STATICCHECK) ./...

$(GOSEC):
	GO111MODULE=on $(GO) install github.com/securego/gosec/v2/cmd/gosec@latest

gosec: $(GOSEC)
	$(GOSEC) ./...

build:
	$(GO) build .

test: build
	$(GO) test -race -coverprofile=coverage.out ./...

$(GOVULNCHECK):
	GO111MODULE=on $(GO) install golang.org/x/vuln/cmd/govulncheck@latest

govulncheck: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

tidy-check:
	$(GO) mod tidy
	@git diff --quiet -- go.mod go.sum || (echo "go.mod/go.sum are not tidy" >&2; exit 1)

lint-extra: staticcheck

security: gosec govulncheck

tools: $(GOLANGCI_LINT) $(STATICCHECK) $(GOSEC) $(GOVULNCHECK)

ci: fmt-check vet lint lint-extra test tidy-check
