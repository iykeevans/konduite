SHELL := /bin/bash
GO_MODULES := backend runtime packages/contracts
BIN := $(CURDIR)/bin

.PHONY: help tools sqlc-check dev infra-up infra-down test test-integration lint lint-go lint-web vet sqlc migrate contracts contracts-check web-dev tidy

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-18s %s\n", $$1, $$2}'

tools: $(BIN)/sqlc $(BIN)/goose $(BIN)/golangci-lint $(BIN)/go-jsonschema ## Build pinned dev tools into ./bin

$(BIN)/sqlc: tools/go.mod
	cd tools && GOWORK=off go build -o $(BIN)/ github.com/sqlc-dev/sqlc/cmd/sqlc
$(BIN)/goose: tools/go.mod
	cd tools && GOWORK=off go build -o $(BIN)/ github.com/pressly/goose/v3/cmd/goose
$(BIN)/golangci-lint: tools/go.mod
	cd tools && GOWORK=off go build -o $(BIN)/ github.com/golangci/golangci-lint/v2/cmd/golangci-lint
$(BIN)/go-jsonschema: tools/go.mod
	cd tools && GOWORK=off go build -o $(BIN)/ github.com/atombender/go-jsonschema

.PHONY: agenthub
AGENTHUB_LDFLAGS := -X github.com/iykeevans/konduite/runtime/internal/version.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
	-X github.com/iykeevans/konduite/runtime/internal/version.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) \
	-X github.com/iykeevans/konduite/runtime/internal/version.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
agenthub: ## Build the runtime CLI into ./bin/agenthub with version info
	cd runtime && go build -ldflags "$(AGENTHUB_LDFLAGS)" -o $(BIN)/agenthub ./cmd/agenthub

dev: infra-up ## Start local infrastructure (Supabase, NATS, OTel, Jaeger); api/worker/web join as they land
	@command -v supabase >/dev/null && supabase status >/dev/null 2>&1 || echo "hint: run 'supabase start' for local Postgres + Auth (PLAT-004)"
	@echo "Jaeger UI: http://localhost:16686  NATS monitor: http://localhost:8222"

infra-up: ## docker compose up -d
	docker compose up -d

infra-down: ## docker compose down
	docker compose down

test: ## Unit tests for every Go module
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done

test-integration: ## Integration tests (testcontainers; build tag 'integration')
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && go test -tags integration ./...) || exit 1; done

vet: ## go vet every module
	@for m in $(GO_MODULES); do (cd $$m && go vet ./...) || exit 1; done

lint: lint-go lint-web ## All linters

lint-go: $(BIN)/golangci-lint ## golangci-lint every module
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && $(BIN)/golangci-lint run ./...) || exit 1; done

lint-web: ## ESLint for the web app
	pnpm --filter web lint

sqlc: $(BIN)/sqlc ## Generate type-safe queries into backend/internal/store
	cd backend && $(BIN)/sqlc generate

migrate: $(BIN)/goose ## Apply migrations: make migrate [CMD=up|down|status]
	cd backend && $(BIN)/goose -dir migrations postgres "$${DATABASE_URL:?set DATABASE_URL}" $${CMD:-up}

sqlc-check: sqlc ## Fail if generated sqlc code is out of date
	@git diff --exit-code -- backend/internal/store || (echo "generated code is stale: run 'make sqlc' and commit"; exit 1)
	@test -z "$$(git ls-files --others --exclude-standard backend/internal/store)" || (echo "untracked generated files in backend/internal/store: run 'make sqlc' and commit"; exit 1)

contracts: $(BIN)/go-jsonschema ## Generate Go + TS types from packages/contracts/schemas
	cd packages/contracts && $(BIN)/go-jsonschema --only-models --struct-name-from-title --capitalization ID --tags json \
		-p contracts -o types.gen.go $$(find schemas -name '*.json' | LC_ALL=C sort)
	pnpm --filter @konduite/contracts generate

contracts-check: contracts ## Fail if generated contract types are out of date
	@git diff --exit-code -- packages/contracts/types.gen.go packages/contracts/ts || (echo "generated contracts are stale: run 'make contracts' and commit"; exit 1)
	@test -z "$$(git ls-files --others --exclude-standard packages/contracts/ts)" || (echo "untracked generated files in packages/contracts/ts: run 'make contracts' and commit"; exit 1)

web-dev: ## Next.js dev server
	pnpm --filter web dev

tidy: ## go mod tidy every module
	@for m in $(GO_MODULES) tools; do (cd $$m && go mod tidy) || exit 1; done
