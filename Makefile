.PHONY: gen build dev test

CORE_BIN := core/bin/depgraph
WEB_DIST := web/dist
STATIC_DIR := core/internal/adapters/http/static

gen:
	cd core && oapi-codegen -config oapi-codegen.yaml ../api/openapi.yaml
	cd web && npx openapi-typescript ../api/openapi.yaml -o src/gen/api.ts

build: gen
	cd web && npm run build
	cp -r $(WEB_DIST)/. $(STATIC_DIR)/
	cd core && go build -o bin/depgraph ./cmd/depgraph

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
