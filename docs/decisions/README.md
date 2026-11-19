# Architecture decision records

Konduite's foundational architecture decisions. Every record uses the same template: status, date and related tickets, then Context, Decision and Consequences. Status is Proposed, Accepted, or Superseded by ADR-NNN.

To change a decision, add a new record that supersedes the old one and update the old record's status. Don't rewrite an accepted record.

| ADR | Decision | Status |
| --- | --- | --- |
| [ADR-001](ADR-001-modular-monolith-with-api-and-worker.md) | Modular monolith with separate API and worker processes | Accepted |
| [ADR-002](ADR-002-postgresql-source-of-truth-goose-sqlc.md) | PostgreSQL as the source of truth, with goose migrations and sqlc queries | Accepted |
| [ADR-003](ADR-003-supabase-auth-verified-by-backend.md) | Supabase Auth for users, verified by the backend | Accepted |
| [ADR-004](ADR-004-transactional-outbox.md) | Transactional outbox for every event | Accepted |
| [ADR-005](ADR-005-nats-jetstream-event-distribution.md) | NATS JetStream for event distribution | Accepted |
| [ADR-006](ADR-006-at-least-once-idempotent-delivery.md) | Push-based, at-least-once delivery with idempotent consumers | Accepted |
| [ADR-007](ADR-007-runtime-websocket-connection.md) | Persistent runtime WebSocket using coder/websocket | Accepted |
| [ADR-008](ADR-008-local-runtime-connector.md) | Local runtime connector (`agenthub`) with bbolt state and OS keychain credentials | Accepted |
| [ADR-009](ADR-009-approval-first-execution.md) | Approval-first execution by default | Accepted |
| [ADR-010](ADR-010-authorization-through-authz-can.md) | Authorization through a single `authz.Can` entry point | Accepted |
| [ADR-011](ADR-011-json-schema-contracts.md) | JSON Schema contracts with generated Go and TypeScript types | Accepted |

## Template

```markdown
# ADR-NNN: Title

- Status: Proposed | Accepted | Superseded by ADR-NNN
- Date: YYYY-MM-DD
- Tickets: ...

## Context

## Decision

## Consequences
```
