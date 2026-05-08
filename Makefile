.PHONY: build test install clean

BIN     := ralph
PREFIX  := $(HOME)/.local/bin
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BIN) ./cmd/ralph

test:
	go test ./...

install: build
	mkdir -p $(PREFIX)
	install -m 0755 bin/$(BIN) $(PREFIX)/$(BIN)

clean:
	rm -rf bin
	go clean -testcache
