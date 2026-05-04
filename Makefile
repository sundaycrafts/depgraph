.PHONY: gen build dev test

CORE_BIN := core/bin/depgraph
WEB_DIST := web/dist
STATIC_DIR := core/internal/adapters/http/static

# VERSION is injected at link time so `depgraph version` reflects the build.
# Override on the command line for explicit values; otherwise fall back to
# `git describe` and finally to "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/sundaycrafts/depgraph/internal/version.Version=$(VERSION)

gen:
	cd core && oapi-codegen -config oapi-codegen.yaml ../api/openapi.yaml
	cd web && npx openapi-typescript ../api/openapi.yaml -o src/gen/api.ts

build: gen
	cd web && npm run build
	cp -r $(WEB_DIST)/. $(STATIC_DIR)/
	cd core && go build -ldflags="$(LDFLAGS)" -o bin/depgraph ./cmd/depgraph

DEPGRAPH_ARGS ?=

# Resolve TARGET_DIR: absolute paths are passed through; relative paths are
# anchored at $PWD (where `make` was invoked) before we cd into core/.
TARGET_DIR_RESOLVED := $(if $(TARGET_DIR),$(if $(filter /%,$(TARGET_DIR)),$(TARGET_DIR),$(PWD)/$(TARGET_DIR)))

dev:
	cd core && go run ./cmd/depgraph $(TARGET_DIR_RESOLVED) $(DEPGRAPH_ARGS) & \
	# arbitrary wait for the backend to start
	sleep 3 && \
	cd web && npm run dev

test:
	cd core && go test ./...
	cd web && npm test
