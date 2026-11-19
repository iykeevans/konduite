# ADR-011: JSON Schema contracts with generated Go and TypeScript types

- Status: Accepted
- Date: 2026-11-19
- Tickets: PLAT-011, MSG-006, RT-004

## Context

The same payloads cross three codebases: the Go backend, the Go runtime and the TypeScript dashboard. They travel as NATS events and WebSocket frames, where no compiler checks them. Hand-maintained copies of each type drift, and a drifted field turns into a silent delivery bug.

## Decision

- JSON Schema files in `packages/contracts/schemas` are the single source of truth for event and WebSocket payloads, starting with the event envelope.
- `make contracts` generates Go structs (in the `packages/contracts` module, shared through `go.work`) and TypeScript types for the web app.
- A validator rejects invalid frames and events at the boundary, and events are validated against their schema before they are published.
- REST endpoints are described separately in OpenAPI 3.1 (`docs/api/openapi.yaml`).

## Consequences

- A payload change starts with a schema change, and the regenerated code is committed with it.
- Malformed frames fail at the edge instead of deep inside a consumer.
- There are two contract formats, JSON Schema for events and frames and OpenAPI for REST. Keeping them aligned where they overlap is manual.
- The code generators aren't chosen yet. PLAT-011 picks them.
