---
name: agent-identity
description: "How agents are identified from runtime.hello, why unannounced agents go offline instead of being deleted, and the composite workspace FKs: read before touching agents or agent_project_access"
metadata:
  type: project
  modified: 2026-11-19
---

# Agent identity

Decided in AGENT-001 (#65), 2026-11-19. Code: `backend/internal/agents`, `backend/migrations/00004_agents.sql`.

- **Key:** an agent is `(runtime_id, provider, name)` from the `runtime.hello` frame. `agents.Service.SyncFromHello` upserts on that key, so a reconnect keeps the agent's id. The runtime and workspace ids come from the verified runtime token, never the frame.
- **Unannounced agents go offline, not away.** Deleting them would cascade away their `agent_project_access` grants whenever an adapter is briefly missing. Rows are only removed when the runtime is revoked (FK cascade).
- **Same-workspace is enforced by the schema.** `agents (runtime_id, workspace_id)` and `agent_project_access (agent_id|project_id, workspace_id)` are composite FKs onto `UNIQUE (id, workspace_id)` constraints, so a grant can't point at a project in another workspace even if a handler gets it wrong.
- **Sync locks the runtime row** (`FOR NO KEY UPDATE`) so two hellos from one runtime can't interleave. A runtime that is gone or in another workspace returns `errs.NotFound`.

**How to apply:** new agent-scoped tables should carry `workspace_id` and use the same composite FK. AGENT-004 owns status beyond online/offline; a re-announce currently resets `busy` to `online`.

Related: [[workspace-authorization]], [[user-ids-have-no-auth-fk]]
