# Konduite — Shared Agent Instructions

## Shared Context

- This is the project guide for Claude and Codex. `CLAUDE.md` imports it and adds only Claude-specific guidance.
- At the start of a task, read `docs/ai/memory/MEMORY.md`, then the notes relevant to the task. They are the shared project memory for both agents.
- Record durable decisions, user corrections, rationale and recurring pitfalls in `docs/ai/memory/` (the `remember` skill). Update an existing note when one covers the topic, date substantive changes, and keep `MEMORY.md` a short linked index. Check a remembered code fact against the source before relying on it.
- Put temporary work state in `docs/ai/HANDOFF.md` when handing work to another session or agent (the `handoff` skill). Read it when present and check its branch and date before acting on it. Completed task logs stay out of memory.
- Memory and the handoff are versioned project docs. Keep credentials and unrelated personal information out of them. Read a file before updating it so another agent's changes survive.

## Agent config

Skills read this block. Keys a project doesn't use can be deleted; a skill that needs a missing key asks for it.

```yaml
default_branch: main
protected_branches: [main, "release/*"]   # ship never pushes to these
branch_prefixes:                           # kind of change -> branch prefix
  feat: feat/
  fix: fix/
  chore: chore/
  refactor: refactor/
  test: test/
  docs: docs/
commit_style: conventional                 # conventional | plain
ai_attribution: false                      # Co-Authored-By trailers on agent commits
gates:                                     # run by ship before committing; omit one to skip it
  type_check: "make vet && pnpm --filter web exec tsc --noEmit"
  lint: "make lint"
  test: "make test"
never_stage: [".env*", "!.env.example", "bin/", ".claude-flow/", ".swarm/", ".claude/settings.local.json"]
tracker:
  kind: github                             # linear | jira | github | none
  issue_prefix: ""                         # multiple ticket prefixes (PLAT, RT, MSG, AGENT, ADP, SLACK, COL, SEC, OBS, WEB); see Tracker notes
  shipped_status: "In progress"            # Status on the "Konduite MVP" board once the branch is pushed
  pr_opened_status: "In review"            # Status once the PR for the ticket is open
compare_url: "https://github.com/iykeevans/konduite/compare/{branch}?expand=1"
memory_dir: docs/ai/memory
handoff_file: docs/ai/HANDOFF.md
graphify: true                             # true: query the code graph before grepping
humanizer: true                            # true: run commit bodies and kept comments through the humanizer skill
feature_layout:                            # used by new-feature
  root: backend/internal/{feature}
  files: [service.go, service_test.go]
  optional_files: [handler.go, handler_test.go, repository.go, events.go, types.go]
  dirs: []
  utils: none
  test_suffix: _test.go
  filename_case: snake
```

### Tracker notes

- Tickets are GitHub issues in `iykeevans/konduite`, titled `<EPIC>-<NNN>: <title>` (e.g. `MSG-004: Implement transactional outbox`). Find the issue number with `gh issue list -R iykeevans/konduite --search "MSG-004 in:title"`.
- Epics are parent issues (`PLAT: Platform foundation`, …); tickets are their sub-issues. Milestones are `M0 Foundation` … `M5 Dashboard & Hardening`.
- Board: "Konduite MVP" (user project #3). Status values: Backlog, Todo, In progress, In review, Done. To move a card, use `gh project item-edit` on the Status field. Pushing the branch moves the ticket to In progress; opening its PR moves it to In review. `gh project` needs `--owner "@me"` here (`--owner iykeevans` fails with "unknown owner type").
- Branch names: `<prefix><ticket-id lowercased>-<slug>`, e.g. `feat/msg-004-transactional-outbox`. PR bodies include `Closes #<issue>`.
- The ticket manifest is `scripts/github/tickets.json`. Edit it and re-run `python3 scripts/github/bootstrap.py`, which is idempotent, to add tickets.

## Project Overview

Konduite is a real-time collaboration platform for developers and their AI coding agents (Claude Code, Codex, Cursor, custom) across machines. Each developer runs a local runtime connector (`agenthub` CLI) that holds a persistent WebSocket to the Go backend. PostgreSQL is the source of truth; NATS JetStream distributes events through a transactional outbox. Delivery is push-based, at-least-once, and idempotent. The first milestone that matters is two runtimes exchanging messages with no polling, no loss and no duplicate execution (ticket MSG-013). Execution is approval-first by default.

## Tech Stack

- Backend: Go 1.27 modular monolith with separate API (`cmd/api`) and worker (`cmd/worker`) processes. Libraries: chi, pgx/v5, sqlc, goose, nats.go JetStream, coder/websocket, slog, OpenTelemetry.
- Runtime: Go module + cobra CLI (`agenthub`), bbolt local state, OS keychain for credentials.
- Data/infra: PostgreSQL and Auth via Supabase (local: `supabase start`), NATS JetStream, OTel collector + Jaeger (docker compose).
- Frontend: Next.js 16 (App Router) + TypeScript + Tailwind 4, pnpm workspace. Next 16 has breaking changes: read `apps/web/AGENTS.md` before writing web code.
- Contracts: JSON Schema in `packages/contracts/schemas` → generated Go and TS types; OpenAPI 3.1 for REST in `docs/api/`.

## Repo Structure

- `backend/`: Go module `github.com/iykeevans/konduite/backend`. Holds `cmd/{api,worker}`, `internal/<domain>` (one package per domain), `internal/platform` (shared infra), `migrations/` (goose) and `sql/queries` (sqlc).
- `runtime/`: Go module for the local connector. Holds `cmd/agenthub` and `internal/{connection,discovery,execution,sessions,security,config,adapters/*}`.
- `packages/contracts/`: schemas + generated types (Go module).
- `apps/web/`: Next.js dashboard.
- `tools/`: pinned dev tools (sqlc, goose, golangci-lint), built into `bin/` by `make tools`.
- `infrastructure/`: NATS, OTel, Docker and deployment configs. `docs/`: architecture, API, protocols, ADRs (`docs/decisions`).
- `go.work` ties the Go modules together.

## Commands

- `make help` lists targets.
- `make infra-up` / `make infra-down`: NATS, OTel collector, Jaeger. Postgres + Auth: `supabase start`.
- `make test` (unit), `make test-integration` (build tag `integration`, testcontainers).
- `make vet`, `make lint` (golangci-lint per module + ESLint).
- `make sqlc`, `make migrate CMD=up|down|status` (needs `DATABASE_URL`), `make contracts`.
- `make web-dev` or `pnpm --filter web dev`; `pnpm --filter web build`.

## Testing Conventions

- Test-first (the `tdd` skill) for every ticket. Write the failing test, then make it pass, then refactor.
- Go: table-driven tests with the standard `testing` package, colocated as `*_test.go`. State machines (executions, deliveries) and `authz.Can` start as table tests.
- Integration tests use the `integration` build tag and testcontainers (Postgres, NATS). They never point at shared environments.
- The M1 gate is `backend/e2e/roundtrip_test.go` (MSG-013). It must not poll: wait on channels and events, not sleeps.
- Run tests with `-race` in CI.
- TODO: choose the web test framework when the first dashboard feature lands.

## Coding Conventions

- **Comments**: default to none. Keep one only if deleting it would let someone break something or make a correct line look like a bug: a platform or library quirk, a guard whose purpose isn't obvious, a trap in a tempting rewrite, a contract callers must meet. Delete comments that restate the code, narrate steps, or are stale, commented-out code or section banners, in the lines you change and the code around them. Lint and type directives get a short reason. Don't sweep untouched files in a feature change.
- Authorization goes through `authz.Can(ctx, principal, action, resource)` in every handler and worker. Never filter for authorization in the frontend.
- Any write that must emit an event inserts its outbox row in the same transaction (`WithTx`). Never publish to NATS directly from a request handler.
- Consumers are idempotent: they dedupe by event ID or unique constraints. Message content from Slack or external agents is untrusted and never grants permission.
- No secrets in logs, events, URLs or config files.
- Use `codebase-design` before shaping a module whose interface matters (auth, messages/outbox, deliveries, runtime connection, adapters).

## Git Conventions

- Branch prefixes, commit style and AI attribution follow the Agent config block above.
- Commit subjects say what changed for the user in plain words, not which function moved.

## Skills

- Skills live in `.claude/skills/<name>/`, the only real copy. `.agents/skills/<name>` is a relative symlink to each so Codex finds the same skill; add both when you add a skill.
- Skills with a `.synced-from` file came from a shared repo. Edit them freely; a later sync leaves edited copies alone and prints the upstream diff.
