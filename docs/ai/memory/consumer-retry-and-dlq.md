---
name: consumer-retry-and-dlq
description: "How worker consumers retry and dead-letter (nats.Client.Consume), DLQ headers and message ids, replay semantics and the platform-admin allow-list: read before writing any JetStream consumer or admin endpoint"
metadata:
  type: project
  modified: 2026-11-20
---

# Consumer retry and dead-letter

Decided in MSG-012 (#62), 2026-11-20. Code: `backend/internal/platform/nats/consume.go`, `dlq.go`, `backend/internal/deadletters`, `backend/internal/admin`.

- **Every worker consumer runs through `Client.Consume(ctx, ConsumerSpec, Handler)`.** Nil acks, an error naks with the spec's backoff (`NakWithDelay`), `nats.Permanent(err)` or the last attempt (default 5) dead-letters. Handler panics count as a failed attempt. Register it with `reg.Add(name, func(ctx) error { return bus.Consume(ctx, spec, h) })`.
- **Explicit DLQ publish, not the MAX_DELIVERIES advisory.** The advisory carries only the stream sequence, so the message would have to be fetched back and could already be gone; publishing from the runner keeps the payload, error and attempt count together and is testable. The server's `MaxDeliver` is the spec's plus 3, so a crash on the final attempt is redelivered and dead-lettered by the runner instead of dropped by the server.
- **Order is publish to `dlq.<subject>`, then `Term`.** The DLQ message id is `dlq:<durable>:<stream seq>`, so a crash between the two re-publishes into JetStream's dedupe. Two consumers failing the same event make two entries.
- **Error text in `Konduite-Dlq-Error` goes through `log.Scrub` and is cut to 512 bytes.** Don't put secrets in handler errors anyway.
- **Replay** republishes to the original subject with `Nats-Msg-Id: replay:<dlq seq>` and the original id in `Konduite-Event-Id`, then deletes the entry. Reusing the original id inside the 2-minute dedupe window would be silently dropped. `Delivery.EventID` prefers `Konduite-Event-Id`, so dedupe-by-event-id keeps working. Every consumer on the subject sees the replay, not only the one that failed.
- **Admin endpoints use `admin.Allowlist` (`PLATFORM_ADMIN_USER_IDS`), not `authz.Can`.** The DLQ spans workspaces, so no workspace role fits. Empty denies everyone, non-admins get 404. The list omits payloads.

Related: [[api-envelope-and-error-mapping]], [[domain-handler-pattern]]
