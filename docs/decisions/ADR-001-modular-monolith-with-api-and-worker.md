# ADR-001: Modular monolith with separate API and worker processes

- Status: Accepted
- Date: 2026-11-19
- Tickets: PLAT-001, PLAT-002, PLAT-003

## Context

Konduite has about fifteen domains (workspaces, projects, agents, runtimes, conversations, messages, deliveries, executions, Slack, tasks and more) and a small team. The work splits into two kinds: request/response handling, including long-lived runtime WebSockets, and background work such as the outbox relay, delivery fan-out, the offline sweeper and Slack sync. Microservices would add network hops, deployment units and distributed failure modes before the product has proved its core round trip.

## Decision

The backend is one Go module (`github.com/iykeevans/konduite/backend`) built into two binaries:

- `cmd/api` serves HTTP under `/api/v1` with chi, health endpoints and, later, the runtime and dashboard WebSockets.
- `cmd/worker` runs named consumers from a registry (`internal/worker`) and drains in-flight work on shutdown.

Both share the `internal/<domain>` packages and `internal/platform` (config, db, httpx, errs). Domains talk to each other through Go interfaces in process and through events, never through HTTP calls to themselves.

## Consequences

- One codebase, one migration set and one set of domain rules. A change to a domain is visible to both processes at compile time.
- The API and worker scale and restart independently. A stuck consumer can't stall request handling, and an API deploy doesn't stop the relay.
- Both processes need the same config and database access, and both must shut down gracefully on SIGTERM.
- Domain boundaries are a code convention, not a network boundary. Keeping them clean is a review responsibility.
- A service could later be split out along a domain package boundary, but nothing is designed for that now.
