.PHONY: build test install clean fmt vet lint tidy check

BIN     := ralph
PREFIX  := $(HOME)/.local/bin
VERSION  = $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BIN) ./cmd/ralph

test:
	go test ./...

install: build
	mkdir -p $(PREFIX)
	install -m 0755 bin/$(BIN) $(PREFIX)/$(BIN)

fmt:
	@gofmt -w $$(find . -type d -name tmp -prune -o -type f -name '*.go' -print)

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; skipping (install: https://golangci-lint.run/welcome/install/)"; \
	fi

tidy:
	go mod tidy

check: fmt vet lint test

clean:
	rm -rf bin
	rm -f coverage.*
	go clean -testcache
