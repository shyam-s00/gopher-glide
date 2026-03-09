## gg — Gopher Glide
## Local build script. Mirrors exactly what the GHA release workflow does.
##
## Usage:
##   make build              → build for current platform (dev build)
##   make build-all          → cross-compile for all platforms into dist/
##   make release            → build-all + package each into dist/
##   make clean              → remove dist/ and local binary
##   make run                → build and run with config.yaml
##   make version            → print version info

# ── Variables ─────────────────────────────────────────────────────────────────
BINARY  := gg
MODULE  := gopher-glide
CMD     := ./cmd/gg
DIST    := dist
ASSETS  := config.yaml request.http

# VERSION, GIT_COMMIT, BUILD_DATE can be overridden by env (GHA sets them).
# Locally they are computed from git.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0-dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
  -X '$(MODULE)/internal/version.Version=$(VERSION)'      \
  -X '$(MODULE)/internal/version.GitCommit=$(GIT_COMMIT)' \
  -X '$(MODULE)/internal/version.BuildDate=$(BUILD_DATE)'

# ── Targets ───────────────────────────────────────────────────────────────────

.PHONY: all build build-all release clean run version \
        build-linux-amd64 \
        build-darwin-arm64 \
        build-windows-amd64

all: build

## build: compile for the current OS/ARCH
build:
	@echo "→ Building $(BINARY) ($(VERSION)) for current platform..."
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@echo "✓ $(BINARY) ready"

# ── Per-platform targets — called directly by GHA matrix build step ───────────

build-linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)


build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@codesign --sign - --force --preserve-metadata=entitlements,requirements,flags,runtime $(BINARY) 2>/dev/null || true

build-windows-amd64:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY).exe $(CMD)

## build-all: cross-compile all platforms into dist/
build-all:
	@mkdir -p $(DIST)
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64       $(CMD)
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64      $(CMD)
	@codesign --sign - --force --preserve-metadata=entitlements,requirements,flags,runtime $(DIST)/$(BINARY)-darwin-arm64 2>/dev/null || true
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe $(CMD)
	@echo "✓ All binaries in $(DIST)/"

## release: build-all + package each platform (tar.gz unix, zip windows)
release: build-all
	@echo "→ Packaging release archives..."
	@for plat in linux-amd64 darwin-arm64; do \
		PKG="$(DIST)/$(BINARY)-$(VERSION)-$$plat"; \
		mkdir -p "$$PKG"; \
		cp "$(DIST)/$(BINARY)-$$plat" "$$PKG/$(BINARY)"; \
		cp $(ASSETS) "$$PKG/"; \
		tar -czf "$$PKG.tar.gz" -C $(DIST) "$(BINARY)-$(VERSION)-$$plat"; \
		rm -rf "$$PKG"; \
		echo "  ✓ $$PKG.tar.gz"; \
	done
	@PKG="$(DIST)/$(BINARY)-$(VERSION)-windows-amd64"; \
		mkdir -p "$$PKG"; \
		cp "$(DIST)/$(BINARY)-windows-amd64.exe" "$$PKG/$(BINARY).exe"; \
		cp $(ASSETS) "$$PKG/"; \
		cd $(DIST) && zip -r "$(BINARY)-$(VERSION)-windows-amd64.zip" "$(BINARY)-$(VERSION)-windows-amd64" && cd ..; \
		rm -rf "$$PKG"; \
		echo "  ✓ $$PKG.zip"
	@echo "✓ Release packages:"
	@ls -lh $(DIST)/*.tar.gz $(DIST)/*.zip 2>/dev/null

## clean: remove build artefacts
clean:
	@rm -rf $(DIST) $(BINARY) $(BINARY).exe
	@echo "✓ Cleaned"

## run: build and run locally with config.yaml
run: build
	@./$(BINARY) config.yaml

## version: show what gets embedded in the binary
version:
	@echo "Version:    $(VERSION)"
	@echo "GitCommit:  $(GIT_COMMIT)"
	@echo "BuildDate:  $(BUILD_DATE)"
