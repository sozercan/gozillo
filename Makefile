GO ?= go
GOFMT ?= gofmt
BINARY ?= gozillo
COMMAND ?= ./cmd/gozillo
PACKAGES ?= ./...

.DEFAULT_GOAL := build

.PHONY: build install clean edge-cdp deps mod-verify tidy fmt fmt-check scripts-check vet test test-race data-boundary check ci help

build: ## Build the gozillo CLI
	$(GO) build -trimpath -o $(BINARY) $(COMMAND)

install: ## Install the gozillo CLI with go install
	$(GO) install $(COMMAND)

clean: ## Remove the local CLI binary
	$(RM) $(BINARY)

edge-cdp: ## Start Microsoft Edge with local CDP enabled
	./scripts/open-edge-cdp.sh

deps: ## Download Go module dependencies
	$(GO) mod download

mod-verify: ## Verify downloaded Go modules
	$(GO) mod verify

tidy: ## Synchronize go.mod and go.sum
	$(GO) mod tidy

fmt: ## Format all Go source files
	$(GOFMT) -w .

scripts-check: ## Validate shell script syntax
	bash -n scripts/*.sh

fmt-check: ## Fail if any Go source file needs formatting
	@files="$$($(GOFMT) -l .)"; \
	if [ -n "$$files" ]; then \
		printf 'The following files need gofmt:\n%s\n' "$$files"; \
		exit 1; \
	fi

vet: ## Run go vet
	$(GO) vet $(PACKAGES)

test: ## Run the test suite
	$(GO) test -count=1 $(PACKAGES)

test-race: ## Run the test suite with the race detector
	$(GO) test -race -count=1 $(PACKAGES)

data-boundary: ## Reject tracked raw captures and generated reports
	@forbidden="$$(git ls-files | grep -E '(^|/)(captures|reports)/|\.raw\.har$$|(^|/)zillow\.har$$' || true)"; \
	if [ -n "$$forbidden" ]; then \
		printf 'Forbidden raw or generated data is tracked:\n%s\n' "$$forbidden"; \
		exit 1; \
	fi

check: data-boundary mod-verify fmt-check scripts-check vet test-race build ## Run local CI-equivalent checks

ci: deps check ## Download dependencies and run CI-equivalent checks

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
