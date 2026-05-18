BIN     := ralph
PREFIX  := $(HOME)/.local/bin
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

GOLANGCI_LINT_VERSION := v1.62.2

.DEFAULT_GOAL := help

.PHONY: help build install test test-race cover bench lint fmt fmt-check tidy tidy-check tools clean check fixtures

help: ## Print this help
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk -F':.*## ' '{ printf "  %-14s %s\n", $$1, $$2 }' | \
		sort

build: ## Compile the ralph binary into bin/
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BIN) ./cmd/ralph

install: build ## Install ralph into $$HOME/.local/bin
	mkdir -p $(PREFIX)
	install -m 0755 bin/$(BIN) $(PREFIX)/$(BIN)

test: ## Run the test suite
	go test ./...

test-race: ## Run the test suite with the race detector
	go test -race -count=1 ./...

cover: ## Generate coverage.out and print a summary
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 1

bench: ## Run all benchmarks
	go test -bench=. -benchmem -run=^$$ ./...

lint: ## Run golangci-lint (skipped with a hint if it is not installed)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; run 'make tools' to install (or see https://golangci-lint.run/welcome/install/)"; \
	fi

fmt: ## Format every Go file in the module
	gofmt -w $$(go list -f '{{.Dir}}' ./...)

fmt-check: ## Fail if any Go file is not gofmt-clean
	@out=$$(gofmt -l $$(go list -f '{{.Dir}}' ./...)); \
	if [ -n "$$out" ]; then \
		echo "gofmt would reformat the following files:"; \
		echo "$$out"; \
		exit 1; \
	fi

tidy: ## Run go mod tidy
	go mod tidy

tidy-check: ## Fail if go mod tidy would change go.mod or go.sum
	@cp go.mod go.mod.bak; cp go.sum go.sum.bak; \
	go mod tidy; \
	rc=0; \
	if ! diff -q go.mod go.mod.bak >/dev/null || ! diff -q go.sum go.sum.bak >/dev/null; then \
		echo "go.mod or go.sum changed under 'go mod tidy'; commit the result"; \
		rc=1; \
	fi; \
	mv go.mod.bak go.mod; mv go.sum.bak go.sum; \
	exit $$rc

tools: ## Install the dev tools used by lint, fmt, and codegen
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/tools/cmd/stringer@latest

check: test-race lint fmt-check tidy-check ## Run every check CI runs

clean: ## Remove build and coverage artifacts
	rm -rf bin
	rm -f coverage.*
	go clean -testcache

# fixtures regenerates internal/stream/testdata/ from LIVE pi (Q14(d)).
#
# This is NOT part of `make test` and is intentionally not in `check`.
# Run it only when pi's `-p --mode json` event format drifts (pi is 0.x;
# its event vocabulary moves fast) and the frozen captures no longer
# match reality — usually signalled by TestLive_PiSmoke failing or the
# stream tests breaking after a pi upgrade.
#
# COST/PREREQS: every capture is a REAL `pi -p --mode json` run against
# live oauth. It spends real API budget and requires `pi` installed and
# authed (~/.pi/agent/auth.json). CI/unauthed environments cannot run it
# and must not. The script redirects pi's stdin from /dev/null (pi hangs
# forever on an unclosed stdin — a prior capture confirmed this) and
# derives truncated.jsonl from the freshly captured done.jsonl.
#
# exact-sum.jsonl is the hand-authored Q14(c) deterministic fixture and
# is NEVER regenerated (it lives under internal/loop/testdata/ and the
# script excludes it by name regardless).
fixtures: ## Regenerate internal/stream/testdata from LIVE pi (costs API; needs pi authed)
	internal/stream/testdata/regen.sh
