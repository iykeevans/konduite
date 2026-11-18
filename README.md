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
supabase start        # local Postgres + Auth
make test             # Go unit tests
make lint             # golangci-lint + ESLint
go run ./backend/cmd/api   # API on :8080 (/healthz, /readyz); env: APP_ENV, API_ADDR, API_SHUTDOWN_TIMEOUT
make web-dev          # dashboard on http://localhost:3000
```

Run `make help` to list all targets.

| Service | URL |
|---|---|
| NATS | `nats://localhost:4222`, monitoring at http://localhost:8222 |
| OTLP | gRPC `localhost:4317`, HTTP `localhost:4318` |
| Jaeger UI | http://localhost:16686 |

## Regenerating the project board

```bash
python3 scripts/github/bootstrap.py --dry-run   # preview
python3 scripts/github/bootstrap.py             # idempotent: creates only what is missing
```
