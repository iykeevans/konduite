# ADR-004: Transactional outbox for every event

- Status: Accepted
- Date: 2026-11-19
- Tickets: PLAT-005, MSG-004, MSG-006

## Context

When a write also has to announce itself (a message was created, a runtime connected), publishing to the bus from the request handler is a dual write. The database commit can succeed while the publish fails, or the publish can go out for a transaction that later rolls back. Either way a recipient misses a message or acts on one that doesn't exist.

## Decision

- Any write that must emit an event inserts a row into `outbox_events` in the same transaction as the change, using `WithTx`.
- Request handlers never publish to NATS directly.
- A relay in the worker reads unpublished rows in batches (`FOR UPDATE SKIP LOCKED`, woken by `LISTEN/NOTIFY`), publishes each one with `Nats-Msg-Id` set to the event id, and then sets `published_at`.
- Events share a common envelope (id, type, version, tenant context, correlation and causation ids). Payloads carry no credentials.

## Consequences

- No commit without its event, and no event without its commit.
- The relay can publish an event more than once, for example if it crashes after publishing but before marking the row. JetStream's dedupe window absorbs most of these, and consumers must be idempotent anyway (ADR-006).
- Event latency includes the relay hop. The `LISTEN/NOTIFY` wakeup keeps it small without a tight polling loop.
- `outbox_events` keeps growing and will need a retention job for published rows. That isn't built yet.
