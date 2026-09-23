BIN     := cpa
PKG     := github.com/shichao-wang/cpa
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin

.PHONY: build install upgrade test fmt vet check dist clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/$(BIN)

# Build this checkout and put it at $(BINDIR)/$(BIN) — the same path
# install.sh installs to, so both routes update one binary. The copy is
# staged beside the target and renamed into place, so a running cpa is
# never replaced by a half-written file.
install: build
	@mkdir -p '$(BINDIR)'
	@stage='$(BINDIR)/.$(BIN).tmp.$$$$'; \
		cp bin/$(BIN) "$$stage" && chmod 0755 "$$stage" && mv -f "$$stage" '$(BINDIR)/$(BIN)'
	@echo "installed $(BINDIR)/$(BIN) ($(VERSION))"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) \
		echo "note: $(BINDIR) is not on your PATH; add it to run $(BIN)";; esac

# Update a source install in place: fast-forward this checkout, then rebuild
# and reinstall. Refuses to run over uncommitted changes, so local edits never
# end up half-built into the binary you install.
upgrade:
	@git rev-parse --git-dir >/dev/null 2>&1 || { \
		echo "upgrade: not a git checkout — clone the repo, or re-run install.sh" >&2; exit 1; }
	@git diff --quiet && git diff --cached --quiet || { \
		echo "upgrade: uncommitted changes; commit or stash them first" >&2; exit 1; }
	@before='not installed'; \
		[ -x '$(BINDIR)/$(BIN)' ] && before="$$('$(BINDIR)/$(BIN)' version)"; \
		git pull --ff-only || exit 1; \
		$(MAKE) --no-print-directory install || exit 1; \
		after="$$('$(BINDIR)/$(BIN)' version)"; \
		printf '%s -> %s\n' "$$before" "$$after"

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
