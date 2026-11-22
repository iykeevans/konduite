@AGENTS.md

## Model routing

The project runs on `opusplan`: Opus in plan mode, Sonnet everywhere else. Subagents default to Haiku. Ruflo, Cloudflare and RevenueCat plugins and all hooks are off in this repo (`.claude/settings.json`). See [model routing](docs/ai/memory/model-routing.md) for why.

**Tier A: Opus plans, Sonnet builds.** Tickets that touch auth/authz, messages/outbox, deliveries, runtime connection, adapters, migrations that change the data model, or the MSG-013 e2e gate.
1. Start in plan mode (Opus). Explore with the `scout` agent, use `codebase-design`, write the plan.
2. On approval the session switches to Sonnet. For a big ticket, save the plan to `docs/ai/HANDOFF.md`, `/clear`, and build in a fresh session so the planning context isn't carried along.
3. Build test-first (yourself or via the `implementer` agent), then `/ship`.

**Tier B: Sonnet end to end, no plan mode.** Handlers and CRUD on existing modules, web pages, CI and config, bug fixes with a known cause, schema or contract additions that follow an existing pattern.

**Tier C: Haiku agents.** Lookups in code, issues and the board (`scout`). Type-check, lint and full test runs (`gate-runner`). Board moves, `scripts/github/bootstrap.py`, `make sqlc`, `make contracts`.

**Escalate** when a test fails the same way twice after a real fix attempt, or the design turns out wrong: write the state to `docs/ai/HANDOFF.md`, switch to plan mode, and let Opus re-plan instead of looping on Sonnet.

**Token hygiene:** `/clear` between tickets. Send broad exploration to `scout` rather than reading files in the main thread. `/compact` before context grows large.
