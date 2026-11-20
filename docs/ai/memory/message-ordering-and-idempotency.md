---
name: message-ordering-and-idempotency
description: "How messages get gapless sequence numbers and why a retried write returns the original without using a number: read before touching messages, MSG-003 persistence or MSG-011 ordering"
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

Related: [[domain-handler-pattern]], [[user-ids-have-no-auth-fk]]
