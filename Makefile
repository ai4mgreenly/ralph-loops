.PHONY: build test install clean

BIN    := ralph
PREFIX := $(HOME)/.local/bin

build:
	go build -o bin/$(BIN) ./cmd/ralph

test:
	go test ./...

install: build
	mkdir -p $(PREFIX)
	install -m 0755 bin/$(BIN) $(PREFIX)/$(BIN)

clean:
	rm -rf bin
	go clean -testcache
