# ADR-007: Persistent runtime WebSocket using coder/websocket

- Status: Accepted
- Date: 2026-11-19
- Tickets: RT-004, RT-005, RT-006, RT-009, RT-010, SEC-005

## Context

Runtimes run on developer machines behind NAT and firewalls, so the backend can't connect to them. Push delivery (ADR-006) needs a long-lived, bidirectional channel that the runtime opens outbound, and the server needs to know reliably whether each runtime is online.

## Decision

- Each runtime holds one persistent WebSocket to the API at `/v1/runtime/connect`. Both server and client use the `coder/websocket` library.
- The runtime must send `runtime.hello` within 5 seconds or the server closes the connection. While it is connected, the API instance subscribes to that runtime's NATS subject (ADR-005).
- The runtime sends a heartbeat every 15 seconds and is marked offline after 45 seconds without one. Connects and disconnects are recorded as outbox events.
- After a disconnect the runtime reconnects with exponential backoff and full jitter (0.5s to 30s), and re-authenticates before its token expires. After `hello`, the server replays every unacknowledged delivery, and the runtime reports its in-flight executions so the server can reconcile them.
- Frames are validated against the JSON Schema contracts (ADR-011).
- Revocation closes the live socket through a NATS signal to the API instance holding it, with a distinct close code. The runtime then stops reconnecting and asks the user to log in again.

## Consequences

- No polling, and delivery latency is one push. The target is P95 under 2 seconds from creation to acknowledgement.
- API instances hold state for the life of a socket. Routing to a runtime goes through NATS, so any instance can hold any runtime.
- Long-lived tokens never travel in the URL.
- The timing values above come from the ticket plan and may be tuned once the load tests (OBS-008) have run.
