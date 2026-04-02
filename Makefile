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

