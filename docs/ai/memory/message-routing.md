---
name: message-routing
description: "How message.created becomes message_deliveries rows and runtime wake-ups, why the unique key is the dedupe, and the outbox subject override: read before touching deliveries, MSG-008 push, or any per-recipient event"
metadata:
  type: project
  modified: 2026-11-20
---

# Message routing

Decided in MSG-007 (#57), 2026-11-20. Code: `backend/internal/deliveries`, `backend/migrations/00009_message_deliveries.sql`, `packages/contracts/schemas/events/runtime.deliver.v1.json`.

- **Recipients are every participant except the sender** (matched on kind and id). An agent routes to `agents.runtime_id`, a runtime to itself, both only if they still exist in the conversation's workspace; otherwise they're skipped. Users get a row with `runtime_id` NULL and no wake-up, for a later dashboard push. MSG-008 must filter `runtime_id IS NOT NULL` when pushing.
- **The dedupe is `UNIQUE (message_id, recipient_id)`, not the event id.** Rows and their wake-up events commit in one transaction, and wake-ups are enqueued only for rows this attempt inserted (`ON CONFLICT DO NOTHING RETURNING`). A redelivered or replayed event inserts nothing and so sends nothing. A participant who joined after the first routing does get a row on a replay.
- **One `runtime.deliver` per message and runtime**, carrying `delivery_ids` for all of that runtime's new rows. It holds ids only; the connection loads the rows.
- **Per-recipient subjects use `events.Event.Subject`.** The envelope `type` pattern rejects uuids and selects the payload schema, so the event stays `runtime.deliver` and the outbox row carries `subject = agenthub.rt.<runtime_id>.deliver` (nullable column, CHECK `agenthub.%`). The relay publishes there when set. Enqueue rejects wildcards and empty tokens.
- **Missing message (conversation deleted) is `nats.Permanent`** and dead-letters on the first attempt. Schema failures too.
- **No `authz.Can` in the router**: there is no principal; the sender was authorized when the message was written (MSG-003). Recipient lookups are scoped to the conversation's workspace in SQL, and the composite FKs keep a delivery in its message's conversation and its runtime's workspace.

Related: [[message-ordering-and-idempotency]], [[consumer-retry-and-dlq]], [[agent-identity]]
