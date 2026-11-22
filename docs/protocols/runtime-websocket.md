# Runtime WebSocket protocol

Decided in ADR-007. Server: `backend/internal/gateway`. Client: `runtime/internal/connection`.

## Endpoint

`GET /v1/runtime/connect`, upgraded to a WebSocket. It sits outside `/api/v1` and the Supabase auth chain.

- Authentication: `Authorization: Bearer <runtime access token>` (from `POST /v1/runtime/token`). The token is never accepted in the URL or query string. A missing, forged or expired token gets `401` before the upgrade, using the standard error envelope.
- The runtime, workspace and user come from the token. Frames carry no tenant fields.
- One connection per runtime. A newer connection for the same runtime replaces the old one, which is closed with 4002.

## Frames

Every message is one text frame holding JSON that validates against `packages/contracts/schemas/frame.json`: `id` (uuid), `type`, `version`, `sent_at`, optional `correlation_id`, and `data`, which validates against `frames/<type>.v<version>.json`. Binary messages and frames over 64 KiB (server side) are refused. Both sides validate every frame they receive, and the client validates what it sends.

| Direction | Type | Notes |
| --- | --- | --- |
| runtime to server | `runtime.hello` | Must be the first frame. Announces the runtime version and its agents. |
| runtime to server | `runtime.heartbeat` | After hello (RT-005 sends them). |
| runtime to server | `runtime.event_ack` | After hello (RT-007 sends them). |

## Lifecycle

1. The runtime dials and sends `runtime.hello` immediately.
2. The server closes with 4001 if hello has not arrived within 5 seconds, and with 1008 if the first frame is anything else or is invalid.
3. After hello the server syncs the announced agents and subscribes to `agenthub.rt.<runtime_id>.>` on NATS. The subscription ends when the connection does.
4. The server pings every 30 seconds and drops a peer that does not pong within 10 seconds. The client does the same. WebSocket ping/pong is protocol-level and carries no frame.

## Close codes

| Code | Meaning | Runtime should |
| --- | --- | --- |
| 1000 | Runtime shutting down normally | n/a |
| 1001 | Server shutting down | reconnect |
| 1008 | Invalid or unexpected frame, or binary message | fix the bug, then reconnect |
| 1011 | Server error | reconnect |
| 4001 | No `runtime.hello` within 5 seconds | reconnect |
| 4002 | Replaced by a newer connection for the same runtime | not reconnect |
| 4003 | The token's runtime no longer exists | re-login |

Reconnect backoff is RT-006. Revoked tokens stay valid for their lifetime (up to 15 minutes) until RT-010.
