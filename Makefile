BIN     := cpa
PKG     := github.com/shichao-wang/cpa
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test fmt vet check clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/$(BIN)

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/$(BIN)

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

check: fmt vet test

clean:
	rm -rf bin
