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

dev:
	cd core && go run ./cmd/depgraph $(TARGET_DIR) & \
	cd web && npm run dev

test:
	cd core && go test ./...
	cd web && npm test
