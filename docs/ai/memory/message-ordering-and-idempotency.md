---
name: message-ordering-and-idempotency
description: "How messages get gapless sequence numbers, why a retried write returns the original without using a number, and how the POST/GET message API (MSG-003) authorizes, dedupes and pages: read before touching messages or MSG-011 ordering"
metadata:
  type: project
  modified: 2026-11-20
---

# Message ordering and idempotency

Decided in MSG-002 (#52), 2026-11-20. Code: `backend/internal/messages`, `backend/migrations/00006_messages.sql`.

- **Sequence numbers come from `conversations.last_seq`**, bumped with `UPDATE ... RETURNING` in the message's transaction. The row lock serializes writers per conversation, so numbers are gapless and ordered. The database never fills `sequence_number` itself; a sequence or default would leave gaps on rollback.
- **Use `messages.AppendTx(ctx, tx, n)`** inside the write's `db.WithTx` so the outbox event (`messages.CreatedEvent`) commits with it. It returns `created=false` with the original message on a retry.
- **A retry never uses a number.** AppendTx looks the key up first; if a concurrent retry slips past that, `INSERT ... ON CONFLICT DO NOTHING` returns no row and the savepoint holding the seq bump is rolled back. Tests in `repository_integration_test.go` fail if either path burns a number.
- **The key is `(conversation_id, sender_id, idempotency_key)`**, as the ticket specifies, and the key is NOT NULL (NULLs never conflict). MSG-003 should generate one when the client sends none.
- **Callers must commit promptly and use READ COMMITTED**: the conversation row stays locked until commit, and a racing retry re-reads the winner in a new statement.
- **message.created carries ids, not content.** Consumers load the message by id. This keeps untrusted text off the bus, and Enqueue's credential scan would otherwise reject a message that merely mentions a token.
- **parent_id** uses a composite FK onto `(id, conversation_id)`, so a reply can't point into another conversation.

## Message API (MSG-003, #53, 2026-11-20)

- **`POST /conversations/{id}/messages`** goes through `messages.Service.Post` -> `Repository.Post`, which runs AppendTx, adds the sender as a participant and enqueues message.created in one `db.WithTx`, and returns only after commit. 201 for a new message, **200 with the original** (body ignored) on a retry.
- **Authorization reuses `conversations.Service.Changeable`** (the participants rule: any member for workspace-wide, `project.contribute` for project ones; hidden is 404, readable-but-not-contributable is 403). Messages must not re-implement it. Listing uses `conversations.Service.Get` (read rule).
- **Senders are auto-joined, not refused.** Anyone allowed to contribute could add themselves via the participants API anyway, so a 403 for non-participants would only add a round trip; joining in the same tx guarantees MSG-007 routes replies to them. Joining happens only when the message is created, not on a retry.
- **Sender is always the user principal**; a non-user principal gets 404 (agents posting need their own path once SEC-003 grants exist). API clients may send `text` or `task`, never `system`.
- **Idempotency key comes from the `Idempotency-Key` header** (standard retry-middleware shape, keeps the body pure content). Absent, the service generates a UUID, so that request cannot be deduplicated; clients that retry must send one.
- **`GET .../messages`** pages by sequence number: `after_seq` (public, for resuming) or the opaque `cursor` (`{"after_seq": n}`), not both. Minimal on purpose; MSG-011 owns stronger ordering/replay guarantees.

Related: [[domain-handler-pattern]], [[user-ids-have-no-auth-fk]], [[workspace-authorization]]
