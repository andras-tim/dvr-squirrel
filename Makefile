# The single entry point for local runs and CI: the workflows call these targets only.
GO       ?= go
# Development tools are pinned as go.mod tool directives, apart from the application's dependencies; GoReleaser has a
# module of its own, as its large dependency graph would force other tools onto incompatible versions.
TOOL       := $(GO) tool -modfile=tools/go.mod
GORELEASER := $(GO) tool -modfile=tools/goreleaser/go.mod goreleaser
BIN      := bin/dvr-squirrel
PKG      := ./cmd/dvr-squirrel
IMAGE    ?= dvr-squirrel

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT   ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE     ?= $(shell git log -1 --format=%cI 2>/dev/null)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# The minimal FFmpeg embedded into the linux/amd64 release binary (build tag embedffmpeg), built from a verified
# source tarball by build/ffmpeg/Dockerfile. LGPL-2.1-or-later; see NOTICE.
FFMPEG_VERSION := 8.1.3
FFMPEG_SHA256  := 7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3
FFMPEG_DIR     := build/ffmpeg
FFMPEG_TAR     := $(FFMPEG_DIR)/ffmpeg-$(FFMPEG_VERSION).tar.xz
FFMPEG_BIN     := internal/ffmpeg/bin/ffmpeg-linux-amd64
# The stamp names the version and the recipe's hash, so a changed recipe rebuilds, and a CI cache restore (which
# gives every file a fresh mtime) does not.
FFMPEG_RECIPE  := $(shell cat $(FFMPEG_DIR)/Dockerfile $(FFMPEG_DIR)/configure-flags | sha256sum | cut -c1-12)
FFMPEG_STAMP   := $(FFMPEG_DIR)/out/stamp-$(FFMPEG_VERSION)-$(FFMPEG_RECIPE)

.PHONY: all lint notice-check fmt-check vet staticcheck govulncheck actionlint test test-embedded build build-slim image snapshot release ffmpeg fmt tidy clean

all: lint test build

lint: fmt-check vet staticcheck govulncheck actionlint notice-check

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

staticcheck:
	$(TOOL) staticcheck ./...

govulncheck:
	$(TOOL) govulncheck ./...

actionlint:
	$(TOOL) actionlint

# NOTICE names the embedded FFmpeg; keep it in step with FFMPEG_VERSION.
notice-check:
	@grep -q "FFmpeg $(FFMPEG_VERSION) " NOTICE || { echo "NOTICE does not name FFmpeg $(FFMPEG_VERSION)"; exit 1; }

test:
	$(GO) test -race ./...

# The embedded build's own tests: extraction and a decode with the real binary.
test-embedded: $(FFMPEG_STAMP)
	$(GO) test -race -tags embedffmpeg ./internal/ffmpeg ./cmd/...

# The release binary: static, with the embedded FFmpeg.
build: $(FFMPEG_STAMP)
	CGO_ENABLED=0 $(GO) build -tags embedffmpeg -trimpath -ldflags='$(LDFLAGS)' -o $(BIN) $(PKG)

# Without the embedded FFmpeg (as `go install` builds it).
build-slim:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o $(BIN)-slim $(PKG)

ffmpeg: $(FFMPEG_STAMP)

$(FFMPEG_TAR):
	curl -fsSL -o $@.part https://ffmpeg.org/releases/ffmpeg-$(FFMPEG_VERSION).tar.xz
	echo "$(FFMPEG_SHA256)  $@.part" | sha256sum -c -
	mv $@.part $@

$(FFMPEG_STAMP): | $(FFMPEG_TAR)
	rm -rf $(FFMPEG_DIR)/out
	docker build --build-arg FFMPEG_VERSION=$(FFMPEG_VERSION) --output type=local,dest=$(FFMPEG_DIR)/out $(FFMPEG_DIR)
	mkdir -p $(dir $(FFMPEG_BIN))
	cp $(FFMPEG_DIR)/out/ffmpeg $(FFMPEG_BIN)
	cd $(dir $(FFMPEG_BIN)) && sha256sum $(notdir $(FFMPEG_BIN)) > $(notdir $(FFMPEG_BIN)).sha256
	touch $@

# Local images from source, the same Dockerfile targets GoReleaser uses: $(IMAGE):dev (with ffmpeg), $(IMAGE):dev-slim.
IMAGE_ARGS := --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE)
image: $(FFMPEG_STAMP)
	docker build $(IMAGE_ARGS) --target full -t $(IMAGE):dev .
	docker build $(IMAGE_ARGS) --target slim -t $(IMAGE):dev-slim .

# Release dry run: binaries, archives, checksums, SBOMs and images in dist/, nothing signed or published.
snapshot: $(FFMPEG_STAMP)
	$(GORELEASER) release --snapshot --clean --skip=sign,publish

# Run by the release workflow on a v* tag: publishes the GitHub release and the images.
release: $(FFMPEG_STAMP)
	$(GORELEASER) release --clean

fmt:
	gofmt -w .

tidy:
	$(GO) mod tidy
	cd tools && $(GO) mod tidy
	cd tools/goreleaser && $(GO) mod tidy

clean:
	rm -rf bin dist $(FFMPEG_DIR)/out
