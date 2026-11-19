# ADR-005: NATS JetStream for event distribution

- Status: Accepted
- Date: 2026-11-19
- Tickets: MSG-005, MSG-006, MSG-012

## Context

Events from the outbox (ADR-004) have to reach several consumers: the delivery worker, the API instance holding a runtime's WebSocket, Slack sync, notifications and the dashboard. Those consumers need durable, replayable streams with redelivery and a dead-letter path, and the API needs cheap per-runtime subjects for push delivery.

## Decision

- NATS with JetStream is the event bus. Locally it runs from `docker compose` (`make infra-up`).
- One stream, `AGENTHUB`, covers `agenthub.>` with file storage and a 2-minute dedupe window. Event type `x.y` maps to subject `agenthub.x.y`.
- A separate `AGENTHUB_DLQ` stream holds events that ran out of retries. Consumers set `MaxDeliver` and a backoff.
- Per-runtime notifications go to `agenthub.rt.<runtime_id>.deliver`, which the API instance holding that runtime's socket subscribes to.
- The application creates the streams. The server config doesn't.
- The client wrapper lives in `internal/platform`.

## Consequences

- PostgreSQL stays the source of truth. NATS carries notifications of committed state, so a lost stream can be rebuilt from the database.
- Publishing sets `Nats-Msg-Id` for broker-side dedupe, but consumers still dedupe themselves (ADR-006).
- Poison events stop retrying and stay inspectable, and an admin endpoint can replay them.
- It is one more piece of infrastructure to run, deploy and monitor (consumer lag, DLQ depth).
