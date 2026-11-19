# ADR-009: Approval-first execution by default

- Status: Accepted
- Date: 2026-11-19
- Tickets: SEC-006, ADP-006, COL-005

## Context

A message to an agent can make it run code, edit files and call tools on a developer's machine. Messages come from teammates, other agents and Slack, and some of that content is untrusted (ADR-010). If runtimes ran whatever arrived, the messaging system would be a remote-execution channel.

## Decision

- Execution is approval-first by default. An execution waits in `awaiting_approval` until the runtime's owner approves it.
- The effective mode is the stricter of the runtime's local config and the workspace policy, so neither side alone can loosen it.
- The runtime re-checks its local mode before running, even after the server has marked an execution approved.
- Owners approve or deny from the dashboard or with `agenthub approvals list|approve|deny`.
- Execution state is a state machine with table tests (`internal/executions/state.go`), and every approval is audited.

## Consequences

- Safe by default: nothing runs on a machine without its owner's consent unless both the owner and the workspace allow it.
- In the default mode every execution waits on a person. Faster modes are an explicit opt-in.
- Other kinds of approval (COL-005) reuse this flow instead of building a second one.
