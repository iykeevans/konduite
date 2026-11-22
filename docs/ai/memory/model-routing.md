---
name: model-routing
description: "Why Konduite runs on opusplan with Haiku subagents and ruflo turned off: read before changing .claude/settings.json, adding agents, or deciding which model handles a ticket"
metadata:
  type: feedback
  modified: 2026-11-22
---

# Model routing

Decided 2026-11-22. Opus usage ran out too fast when every session ran on Opus, so the work is split by model:

- `.claude/settings.json` sets `model: opusplan`, meaning Opus only in plan mode and Sonnet for building. It sets `CLAUDE_CODE_SUBAGENT_MODEL=haiku` so subagents without their own `model:` run on Haiku.
- Project agents in `.claude/agents/`: `scout` (Haiku, read-only lookups), `gate-runner` (Haiku, runs gates and returns only failures), `implementer` (Sonnet, builds an approved plan test-first).
- Opus plans hard tickets only (the Tier A list in `CLAUDE.md`). The user chose not to have Opus review diffs.
- Ruflo, Cloudflare and RevenueCat plugins and all hooks (`disableAllHooks`) are off for this repo only. The global `~/.claude/settings.json` still enables them for other projects.

**Why:** the ruflo plugins add hundreds of tool names and inject routing context on every prompt, which costs tokens on any model. In this repo that work is already covered by `docs/ai/memory`, `docs/ai/HANDOFF.md` and the skills.

**How to apply:** don't re-enable ruflo or hooks here without checking with the user. Add new agents with an explicit `model:`. Send exploration and gate runs to the Haiku agents to keep long output out of the main context.
