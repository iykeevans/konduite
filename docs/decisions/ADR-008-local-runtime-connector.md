# ADR-008: Local runtime connector (`agenthub`) with bbolt state and OS keychain credentials

- Status: Accepted
- Date: 2026-11-19
- Tickets: RT-001, RT-002, RT-003, RT-007, RT-008, SEC-004

## Context

AI coding agents (Claude Code, Codex, Cursor, custom) run on developers' own machines against local checkouts. Something on each machine has to discover those agents, hold the connection to the backend, run tasks through them and remember what it has already processed across restarts. It also handles credentials that must not leak.

## Decision

- Each developer runs `agenthub`, a Go CLI built with cobra from the `runtime/` module.
- Configuration lives at `~/.config/agenthub/config.yaml` and never contains plaintext secrets.
- Credentials are stored in the OS keychain. `agenthub login` and `agenthub logout` manage them, and logout also revokes the runtime credential.
- Local durable state (the runtime id, processed event and message ids) is kept in a bbolt file, so dedupe survives restarts (ADR-006).
- A runtime has its own identity, separate from the user's. It registers with a user token and receives a refresh credential, which the server stores hashed. It exchanges that credential for short-lived access tokens (planned in SEC-004: 15 minutes, Ed25519-signed, carrying the runtime, workspace and owner ids).
- Local agents are reached through provider adapters under `runtime/internal/adapters`, detected at startup and announced in `runtime.hello`. The adapter contract itself is shaped in ADP-001.

## Consequences

- No server component reaches into a developer's machine. All traffic is outbound from the runtime.
- bbolt is embedded and needs no daemon, but it locks its file, so only one `agenthub` process can use a state directory at a time.
- A stolen access token is useful for minutes, and revocation stops both refresh and reconnect.
- Keychain behaviour differs by OS, and headless Linux machines may need a fallback. That is still open.
