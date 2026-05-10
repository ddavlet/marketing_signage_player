VER     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
MODULE  := github.com/marketing-signage/player
LDFLAGS := -s -w -X $(MODULE)/internal/system.Version=$(VER)
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
OUT     ?= bin/player-agent

BUILD = GOOS=$(GOOS) GOARCH=$(GOARCH) \
        go build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT) ./cmd/player-agent

# Secrets for `make upload` (set in environment or .env.local)
SIGNAGE_SERVER ?=
SIGNAGE_TOKEN  ?=

.PHONY: build release-all upload test tidy clean

build:
	@mkdir -p $(dir $(OUT))
	$(BUILD)

release-all:
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 OUT=dist/marketing-signage-player-linux-amd64 $(MAKE) build
	GOOS=linux GOARCH=arm64 OUT=dist/marketing-signage-player-linux-arm64 $(MAKE) build
	cd dist && sha256sum marketing-signage-player-linux-* > SHA256SUMS
	@echo "Built dist/ for linux/amd64 and linux/arm64 — version $(VER)"

# Upload dist/ binaries to the control panel as a new release entry.
# Requires SIGNAGE_SERVER and SIGNAGE_TOKEN env vars.
upload: release-all
	@test -n "$(SIGNAGE_SERVER)" || (echo "SIGNAGE_SERVER is not set" >&2; exit 1)
	@test -n "$(SIGNAGE_TOKEN)"  || (echo "SIGNAGE_TOKEN is not set" >&2; exit 1)
	@VER_BARE="$(shell echo $(VER) | sed 's/^v//')"; \
	for ARCH in amd64 arm64; do \
	  BINARY="dist/marketing-signage-player-linux-$${ARCH}"; \
	  SHA256=$$(grep "$${BINARY##*/}" dist/SHA256SUMS | awk '{print $$1}'); \
	  curl -fsSL -X POST "$(SIGNAGE_SERVER)/api/player/releases/" \
	    -H "Authorization: Token $(SIGNAGE_TOKEN)" \
	    -H "Content-Type: application/json" \
	    -d "{\"version\":\"$${VER_BARE}\",\"channel\":\"stable\",\"os\":\"linux\",\"arch\":\"$${ARCH}\",\"download_url\":\"$(SIGNAGE_SERVER)/static/player/marketing-signage-player-linux-$${ARCH}\",\"sha256\":\"$${SHA256}\",\"is_active\":true}" \
	    && echo "Registered $${VER_BARE} linux/$${ARCH}"; \
	done

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf bin dist
