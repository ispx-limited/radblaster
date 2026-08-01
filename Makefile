SHELL := /bin/sh

MODULE  := github.com/ispx-limited/radblaster
BIN_DIR := bin
BIN     := $(BIN_DIR)/radblaster

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)

LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) \
           -X $(MODULE)/internal/version.Commit=$(COMMIT) \
           -X $(MODULE)/internal/version.Date=$(DATE)

.PHONY: all build test test-race bench lint fmt fmt-check vet tidy clean

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/radblaster

test:
	go test ./...

test-race:
	go test -race ./...

bench:
	go test ./... -run '^$$' -bench . -benchmem

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

fmt-check:
	@out="$$(gofmt -s -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needed on:"; echo "$$out"; exit 1; \
	fi

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)
