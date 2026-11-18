SHELL := /bin/bash
GO_MODULES := backend runtime packages/contracts
BIN := $(CURDIR)/bin

.PHONY: help tools sqlc-check dev infra-up infra-down test test-integration lint lint-go lint-web vet sqlc migrate contracts web-dev tidy

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-18s %s\n", $$1, $$2}'

tools: $(BIN)/sqlc $(BIN)/goose $(BIN)/golangci-lint ## Build pinned dev tools into ./bin

$(BIN)/sqlc: tools/go.mod
	cd tools && GOWORK=off go build -o $(BIN)/ github.com/sqlc-dev/sqlc/cmd/sqlc
$(BIN)/goose: tools/go.mod
	cd tools && GOWORK=off go build -o $(BIN)/ github.com/pressly/goose/v3/cmd/goose
$(BIN)/golangci-lint: tools/go.mod
	cd tools && GOWORK=off go build -o $(BIN)/ github.com/golangci/golangci-lint/v2/cmd/golangci-lint

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

contracts: ## Generate Go + TS types from JSON Schemas (PLAT-011)
	@echo "not implemented yet: PLAT-011"

web-dev: ## Next.js dev server
	pnpm --filter web dev

tidy: ## go mod tidy every module
	@for m in $(GO_MODULES) tools; do (cd $$m && go mod tidy) || exit 1; done
