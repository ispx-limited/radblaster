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

# Platforms a release is built for. Windows is deliberately absent: this drives
# load from an operator's Linux or macOS machine, and nothing in it is asked for
# on Windows.
DIST_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build ui dist test test-race bench lint fmt fmt-check vet tidy clean

all: build

# The interface's built assets are committed, so a plain build needs only Go.
# Run "make ui" after changing anything under ui/.
ui:
	cd ui && pnpm install --frozen-lockfile && pnpm build
	rm -rf internal/web/dist
	cp -r ui/dist internal/web/dist

build:
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/radblaster

# dist builds the archives a release is published from. The interface's built
# assets are committed, so this needs nothing but Go, and a release can be
# reproduced locally by running exactly what the workflow runs.
#
# Archive names carry no version, so that the download URL for the latest
# release is a stable one people can paste into a script. The version is in the
# binary, which reports it with "radblaster version".
dist:
	@rm -rf dist && mkdir -p dist
	@for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		name="radblaster_$${os}_$${arch}"; \
		echo "building $$name"; \
		mkdir -p "dist/$$name" || exit 1; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" \
			-o "dist/$$name/radblaster" ./cmd/radblaster || exit 1; \
		cp LICENSE NOTICE README.md "dist/$$name/" || exit 1; \
		tar -C dist -czf "dist/$$name.tar.gz" "$$name" || exit 1; \
		rm -rf "dist/$$name"; \
	done
	@cd dist && sha256sum *.tar.gz > checksums.txt
	@ls -l dist

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
