# Konduite

Real-time collaboration between developers and their AI agents (Claude Code, Codex, Cursor, custom agents) across machines. A local runtime connector on each machine holds a persistent WebSocket to a Go backend. PostgreSQL is the source of truth, and NATS JetStream distributes events.

Roadmap and tickets: [Konduite MVP project board](https://github.com/users/iykeevans/projects/3). The ticket list is in `scripts/github/tickets.json`.

## Layout

```
apps/web/              Next.js + TypeScript dashboard
backend/               Go API (cmd/api) and workers (cmd/worker), modular monolith
runtime/               Go local runtime + `agenthub` CLI (separate module, distributed independently)
packages/contracts/    JSON Schemas -> generated Go/TS event and WebSocket types
tools/                 Pinned dev tools (sqlc, goose, golangci-lint) run via `go tool`
infrastructure/        NATS, OpenTelemetry, Docker, deployment configs
docs/                  architecture, API (OpenAPI), protocols, ADRs
scripts/github/        GitHub Projects bootstrap
```

`go.work` ties the Go modules together for local development.

## Prerequisites

- Go 1.27+
- Node 24+ and pnpm
- Docker
- [Supabase CLI](https://supabase.com/docs/guides/local-development) (local Postgres + Auth)

## Getting started

```bash
pnpm install          # web dependencies
make infra-up         # NATS (JetStream), OTel collector, Jaeger
supabase start        # local Postgres + Auth (first run pulls images; config in supabase/config.toml)
cp .env.example .env  # DATABASE_URL for the local Supabase database
make test             # Go unit tests
make lint             # golangci-lint + ESLint
set -a; . ./.env; set +a; go run ./backend/cmd/api      # API on :8080; /readyz checks Postgres
set -a; . ./.env; set +a; go run ./backend/cmd/worker   # background consumers; drains on SIGTERM
make web-dev          # dashboard on http://localhost:3000
```

Run `make help` to list all targets.

### Database

Local Postgres 17 comes from the Supabase CLI: `supabase start` prints the URLs and keys, `supabase status` shows them again, and `supabase stop` shuts it down. The database is at `postgresql://postgres:postgres@127.0.0.1:54322/postgres`, and Supabase Studio is at http://localhost:54323.

Migrations are goose SQL files in `backend/migrations` (`NNNNN_name.sql` with `-- +goose Up` and `-- +goose Down` sections). Apply them with `make migrate` (`CMD=up|down|status`, default `up`), using `DATABASE_URL` from your environment. Queries live in `backend/sql/queries`. `make sqlc` regenerates `backend/internal/store` from them and the schema. Commit the generated code; CI runs `make sqlc-check` and fails if it is stale.

Integration tests don't use it. `make test-integration` starts a throwaway `postgres:17-alpine` container through testcontainers (Docker must be running), and each test gets its own database cloned from a template that has every migration applied (`backend/internal/platform/dbtest`).

| Service | URL |
|---|---|
| NATS | `nats://localhost:4222`, monitoring at http://localhost:8222 |
| OTLP | gRPC `localhost:4317`, HTTP `localhost:4318` |
| Jaeger UI | http://localhost:16686 |
| Postgres (Supabase) | `postgresql://postgres:postgres@127.0.0.1:54322/postgres` |
| Supabase Studio | http://localhost:54323 |

### Contracts

WebSocket frames and NATS events are defined as JSON Schema in `packages/contracts/schemas`. `frame.json` is the WebSocket frame and `envelope.json` the NATS event; each payload is its own file, `frames/<type>.v<version>.json` or `events/<type>.v<version>.json`. `make contracts` regenerates the Go structs (`packages/contracts/types.gen.go`) and the TS types (`packages/contracts/ts`, imported as `@konduite/contracts`). Commit the generated code; CI runs `make contracts-check` and fails if it is stale. Validate every incoming frame or event with `validate.Frame` / `validate.Event` (`packages/contracts/validate`) before acting on it: an unknown type or version is rejected with `validate.ErrUnknownType`.

## Regenerating the project board

```bash
python3 scripts/github/bootstrap.py --dry-run   # preview
python3 scripts/github/bootstrap.py             # idempotent: creates only what is missing
```
