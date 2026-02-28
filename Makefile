## gg — Gopher Glide
## Local build script. Mirrors exactly what the GHA release workflow does.
##
## Usage:
##   make build          → build for current platform (dev build)
##   make build-all      → cross-compile for linux/macos/windows
##   make release        → build-all + package each platform into dist/
##   make clean          → remove dist/ and local binary
##   make run            → dev run using config.yaml
##   make version        → print current version info

# ── Variables ─────────────────────────────────────────────────────────────────
BINARY      := gg
MODULE      := gopher-glide
CMD         := ./cmd/gg
DIST        := dist

VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE  := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
  -X '$(MODULE)/internal/version.Version=$(VERSION)'      \
  -X '$(MODULE)/internal/version.GitCommit=$(COMMIT)'     \
  -X '$(MODULE)/internal/version.BuildDate=$(BUILD_DATE)'

# Bundled assets copied into every release package
ASSETS := config.yaml request.http

# ── Targets ───────────────────────────────────────────────────────────────────

.PHONY: all build build-all release clean run version

all: build

## build: compile for the current OS/ARCH (dev workflow)
build:
	@echo "→ Building $(BINARY) ($(VERSION)) for current platform..."
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@echo "✓ $(BINARY) ready"

## build-all: cross-compile for all target platforms
build-all:
	@mkdir -p $(DIST)
	@echo "→ linux/amd64"
	GOOS=linux   GOARCH=amd64  go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64      $(CMD)
	@echo "→ linux/arm64"
	GOOS=linux   GOARCH=arm64  go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64      $(CMD)
	@echo "→ darwin/amd64"
	GOOS=darwin  GOARCH=amd64  go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64     $(CMD)
	@echo "→ darwin/arm64  (Apple Silicon)"
	GOOS=darwin  GOARCH=arm64  go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64     $(CMD)
	@echo "→ windows/amd64"
	GOOS=windows GOARCH=amd64  go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe $(CMD)
	@echo "✓ All binaries in $(DIST)/"

## release: build-all + package each platform (tar.gz for unix, zip for windows)
release: build-all
	@echo "→ Packaging release archives..."
	# linux/amd64
	@mkdir -p $(DIST)/pkg/$(BINARY)-$(VERSION)-linux-amd64
	@cp $(DIST)/$(BINARY)-linux-amd64      $(DIST)/pkg/$(BINARY)-$(VERSION)-linux-amd64/$(BINARY)
	@cp $(ASSETS)                           $(DIST)/pkg/$(BINARY)-$(VERSION)-linux-amd64/
	@tar -czf $(DIST)/$(BINARY)-$(VERSION)-linux-amd64.tar.gz   -C $(DIST)/pkg $(BINARY)-$(VERSION)-linux-amd64
	# linux/arm64
	@mkdir -p $(DIST)/pkg/$(BINARY)-$(VERSION)-linux-arm64
	@cp $(DIST)/$(BINARY)-linux-arm64      $(DIST)/pkg/$(BINARY)-$(VERSION)-linux-arm64/$(BINARY)
	@cp $(ASSETS)                           $(DIST)/pkg/$(BINARY)-$(VERSION)-linux-arm64/
	@tar -czf $(DIST)/$(BINARY)-$(VERSION)-linux-arm64.tar.gz   -C $(DIST)/pkg $(BINARY)-$(VERSION)-linux-arm64
	# darwin/amd64
	@mkdir -p $(DIST)/pkg/$(BINARY)-$(VERSION)-darwin-amd64
	@cp $(DIST)/$(BINARY)-darwin-amd64     $(DIST)/pkg/$(BINARY)-$(VERSION)-darwin-amd64/$(BINARY)
	@cp $(ASSETS)                           $(DIST)/pkg/$(BINARY)-$(VERSION)-darwin-amd64/
	@tar -czf $(DIST)/$(BINARY)-$(VERSION)-darwin-amd64.tar.gz  -C $(DIST)/pkg $(BINARY)-$(VERSION)-darwin-amd64
	# darwin/arm64
	@mkdir -p $(DIST)/pkg/$(BINARY)-$(VERSION)-darwin-arm64
	@cp $(DIST)/$(BINARY)-darwin-arm64     $(DIST)/pkg/$(BINARY)-$(VERSION)-darwin-arm64/$(BINARY)
	@cp $(ASSETS)                           $(DIST)/pkg/$(BINARY)-$(VERSION)-darwin-arm64/
	@tar -czf $(DIST)/$(BINARY)-$(VERSION)-darwin-arm64.tar.gz  -C $(DIST)/pkg $(BINARY)-$(VERSION)-darwin-arm64
	# windows/amd64
	@mkdir -p $(DIST)/pkg/$(BINARY)-$(VERSION)-windows-amd64
	@cp $(DIST)/$(BINARY)-windows-amd64.exe $(DIST)/pkg/$(BINARY)-$(VERSION)-windows-amd64/$(BINARY).exe
	@cp $(ASSETS)                            $(DIST)/pkg/$(BINARY)-$(VERSION)-windows-amd64/
	@cd $(DIST)/pkg && zip -r ../$(BINARY)-$(VERSION)-windows-amd64.zip $(BINARY)-$(VERSION)-windows-amd64
	@rm -rf $(DIST)/pkg
	@echo "✓ Release packages:"
	@ls -lh $(DIST)/*.tar.gz $(DIST)/*.zip 2>/dev/null

## clean: remove build artefacts
clean:
	@rm -rf $(DIST) $(BINARY) $(BINARY).exe
	@echo "✓ Cleaned"

## run: build and run locally with config.yaml
run: build
	@./$(BINARY) config.yaml

## version: show version info that would be embedded in the binary
version:
	@echo "Version:    $(VERSION)"
	@echo "Commit:     $(COMMIT)"
	@echo "Build date: $(BUILD_DATE)"

