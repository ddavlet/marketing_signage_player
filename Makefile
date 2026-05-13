VER     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
MODULE  := github.com/marketing-signage/player
LDFLAGS := -s -w -X $(MODULE)/internal/system.Version=$(VER)
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
OUT     ?= bin/player-agent

BUILD = GOOS=$(GOOS) GOARCH=$(GOARCH) \
        go build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT) ./cmd/player-agent

.PHONY: build release-all test tidy clean

build:
	@mkdir -p $(dir $(OUT))
	$(BUILD)

release-all:
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 OUT=dist/marketing-signage-player-linux-amd64 $(MAKE) build
	GOOS=linux GOARCH=arm64 OUT=dist/marketing-signage-player-linux-arm64 $(MAKE) build
	cd dist && sha256sum marketing-signage-player-linux-* > SHA256SUMS
	@echo "Built dist/ for linux/amd64 and linux/arm64 — version $(VER)"

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf bin dist
