BIN     := cpa
PKG     := github.com/shichao-wang/cpa
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test fmt vet check dist clean

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

# Cross-compile the same archives the release workflow publishes, so
# install.sh can be tested locally: make dist && CPA_INSTALL_DIR=/tmp/x ./install.sh
dist:
	rm -rf dist
	@ver=$$(printf '%s' '$(VERSION)' | sed 's/^v//'); \
	for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		name="$(BIN)_$${ver}_$${os}_$${arch}"; \
		mkdir -p "dist/stage/$${name}"; \
		CGO_ENABLED=0 GOOS=$${os} GOARCH=$${arch} \
			go build -trimpath -ldflags '$(LDFLAGS)' -o "dist/stage/$${name}/$(BIN)" ./cmd/$(BIN) || exit 1; \
		cp README.md README.zh-CN.md LICENSE "dist/stage/$${name}/"; \
		cp -R examples "dist/stage/$${name}/"; \
		tar -C dist/stage -czf "dist/$${name}.tar.gz" "$${name}" || exit 1; \
	done
	rm -rf dist/stage
	cd dist && shasum -a 256 *.tar.gz > checksums.txt
	@cat dist/checksums.txt

clean:
	rm -rf bin dist
