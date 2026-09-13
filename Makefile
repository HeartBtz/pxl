.PHONY: build run test lint clean docker docker-up docker-down fmt vet release

# ── Variables ─────────────────────────────────────────
APP_NAME     := pxl
BUILD_DIR    := ./build
MAIN_PKG     := ./cmd/server
GO           := go
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS      := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

# Cross-compilation targets
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

# ── Build ─────────────────────────────────────────────

build:
	@echo ">> Building $(APP_NAME)..."
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_PKG)

run: build
	$(BUILD_DIR)/$(APP_NAME)

# ── Quality ───────────────────────────────────────────

test:
	$(GO) test -race -cover ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 || echo "golangci-lint not installed, skipping."
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || true

# ── Docker ────────────────────────────────────────────

docker:
	docker build -t $(APP_NAME):latest .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f pxl

# ── Cleanup ───────────────────────────────────────────

clean:
	rm -rf $(BUILD_DIR)
	$(GO) clean -cache -testcache

# ── Release (cross-compilation) ──────────────────────

release: clean
	@echo ">> Building $(APP_NAME) $(VERSION) for all platforms..."
	@mkdir -p $(BUILD_DIR)
	@$(foreach platform,$(PLATFORMS), \
		$(eval GOOS   := $(word 1,$(subst /, ,$(platform)))) \
		$(eval GOARCH := $(word 2,$(subst /, ,$(platform)))) \
		$(eval SUFFIX := $(if $(filter windows,$(GOOS)),.exe,)) \
		$(eval OUT    := $(BUILD_DIR)/$(APP_NAME)-$(VERSION)-$(GOOS)-$(GOARCH)$(SUFFIX)) \
		echo "  → $(GOOS)/$(GOARCH)" && \
		CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -ldflags="$(LDFLAGS)" -o $(OUT) $(MAIN_PKG) && \
	) true
	@echo ">> Done. Binaries in $(BUILD_DIR)/"
	@ls -lh $(BUILD_DIR)/
