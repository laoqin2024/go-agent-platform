.PHONY: swagger swagger-clean swagger-check

# You can override these when invoking make, e.g.:
# make swagger SWAGGER_MAIN=scripts/debug_server/main.go SWAGGER_OUT=docs
SWAGGER_MAIN ?= cmd/server/main.go
SWAGGER_OUT ?= internal/server/docs

swagger:
	@# Ensure swag CLI exists
	@if ! command -v swag >/dev/null 2>&1; then \
		echo "Error: 'swag' not found. Install with: go install github.com/swaggo/swag/cmd/swag@latest"; \
		exit 1; \
	fi
	@GOROOT="$$(go env GOROOT)" swag init -g $(SWAGGER_MAIN) -o $(SWAGGER_OUT) --parseDependency
	@echo "Swagger docs generated successfully at '$(SWAGGER_OUT)'."

swagger-clean:
	@rm -rf "$(SWAGGER_OUT)"
	@$(MAKE) swagger

swagger-check:
	@set -e; \
	echo "Checking generated swagger docs in '$(SWAGGER_OUT)'..."; \
	if [ ! -f "$(SWAGGER_OUT)/swagger.json" ]; then \
		echo "Error: $(SWAGGER_OUT)/swagger.json not found. Run 'make swagger' first."; \
		exit 1; \
	fi; \
	if grep -q '"collector.ListeningPort"' "$(SWAGGER_OUT)/swagger.json"; then \
		echo "OK: found collector.ListeningPort"; \
	else \
		echo "Error: collector.ListeningPort not found"; \
		exit 1; \
	fi; \
	if grep -q '"is_high_risk"' "$(SWAGGER_OUT)/swagger.json"; then \
		echo "OK: found is_high_risk"; \
	else \
		echo "Error: is_high_risk not found"; \
		exit 1; \
	fi; \
	echo "Swagger check passed."

APP_NAME ?= agent
MODULE_PATH ?= github.com/qinyilin/go-agent
MAIN_PKG ?= $(MODULE_PATH)/cmd/agent

DIST_DIR ?= dist

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -s -w \
	-X '$(MAIN_PKG).Version=$(VERSION)' \
	-X '$(MAIN_PKG).BuildTime=$(BUILD_TIME)' \
	-X '$(MAIN_PKG).GitCommit=$(GIT_COMMIT)'

GOFLAGS ?= -mod=readonly
CGO_ENABLED ?= 0

.PHONY: all build-windows build-linux build-darwin clean

all: build-windows build-linux build-darwin

clean:
	rm -rf '$(DIST_DIR)'

build-windows:
	mkdir -p '$(DIST_DIR)/windows-amd64'
	CGO_ENABLED=$(CGO_ENABLED) GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o '$(DIST_DIR)/windows-amd64/$(APP_NAME).exe' ./cmd/agent

build-linux:
	mkdir -p '$(DIST_DIR)/linux-amd64'
	CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o '$(DIST_DIR)/linux-amd64/$(APP_NAME)' ./cmd/agent

build-darwin:
	mkdir -p '$(DIST_DIR)/darwin-arm64'
	CGO_ENABLED=$(CGO_ENABLED) GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o '$(DIST_DIR)/darwin-arm64/$(APP_NAME)' ./cmd/agent

