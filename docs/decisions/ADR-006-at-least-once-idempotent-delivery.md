# ADR-006: Push-based, at-least-once delivery with idempotent consumers

- Status: Accepted
- Date: 2026-11-19
- Tickets: MSG-002, MSG-003, MSG-007 to MSG-011, RT-007, MSG-013

## Context

Konduite's core promise is that two runtimes exchange messages with no polling, no loss and no duplicate execution, across disconnects, retries and redeliveries. Exactly-once delivery over a network isn't achievable. At-least-once delivery combined with consumers that have no extra effect the second time is.

## Decision

- Delivery is push-based. Runtimes never poll for messages.
- Delivery is at-least-once. Every hop that can retry does: the outbox relay, JetStream consumers, the API pushing deliveries, and the offline sweeper that re-pushes unacknowledged deliveries.
- Every consumer is idempotent and dedupes by event id or a unique constraint:
  - `messages` has `UNIQUE(conversation_id, sender_id, idempotency_key)`, and a retried POST returns the original message.
  - `message_deliveries` has `UNIQUE(message_id, recipient_id)`.
  - Worker consumers record the event ids they have processed.
  - `inbound_events` has `UNIQUE(provider, external_event_id)` for webhooks.
  - The runtime keeps processed event and message ids in bbolt and re-acks a duplicate without dispatching it.
  - `agent_executions` has `UNIQUE(message_id, agent_id)`.
- A write is acknowledged (HTTP 201) only after it commits. A runtime sends a receipt ack only after recording the event durably. Receipt acks and execution acks are separate.
- Side-effecting endpoints accept an `Idempotency-Key`.
- Per-conversation order comes from a gapless `sequence_number`, and pending deliveries are pushed in conversation and sequence order.

## Consequences

- Duplicates are expected everywhere, and every new consumer has to say how it dedupes. Review checks this.
- The end-to-end gate (`backend/e2e/roundtrip_test.go`, MSG-013) proves the guarantee: an offline recipient, a reconnect, duplicate frames and NATS redelivery must each lead to exactly one adapter invocation. The test waits on channels and events, never on sleeps.
- Unique constraints and dedupe tables cost storage and write time. That cost is accepted.
- A delivery that never gets acknowledged becomes dead after a bounded number of attempts and is surfaced instead of retrying forever.
