VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w
GOFLAGS  = -trimpath

.PHONY: build test install dist clean

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/clippy .

test:
	go test ./...

install:
	CGO_ENABLED=0 go install $(GOFLAGS) -ldflags "$(LDFLAGS)" .

# Static binaries for the usual Plex hosts.
dist:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/clippy-darwin-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/clippy-darwin-amd64 .
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/clippy-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/clippy-linux-arm64 .

clean:
	rm -rf bin dist

# CLI with the linked-FFmpeg engine (needs third_party/build-ffmpeg.sh macos).
FFPC = $(CURDIR)/third_party/build/macos/lib/pkgconfig
build-libav:
	PKG_CONFIG_PATH=$(FFPC) go build $(GOFLAGS) -tags libav -o bin/clippy-libav .

test-libav:
	PKG_CONFIG_PATH=$(FFPC) go test -tags libav ./...

ffmpeg-macos:
	third_party/build-ffmpeg.sh macos
