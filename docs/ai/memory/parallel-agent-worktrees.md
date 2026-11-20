---
name: parallel-agent-worktrees
description: "Two false failures that show up when several agents work in separate git worktrees at once: the web type-check missing LayoutProps, and testcontainers missing their startup deadline. Read before treating a gate failure in a fresh worktree as a code bug"
metadata:
  type: project
  modified: 2026-11-20
---

# Parallel agent worktrees

Seen repeatedly in the wave-1 and wave-2 runs (2026-11), where several agents each had their own worktree under `.claude/worktrees/`.

- **`Cannot find name 'LayoutProps'` in a fresh worktree.** `pnpm --filter web exec tsc --noEmit` fails until Next has generated its route types, which live in the gitignored `apps/web/.next`. A new worktree has none. Run `pnpm install` (if `node_modules` is missing) and then `pnpm --filter web exec next typegen` once, and the type-check passes. CI builds first, so it never sees this. Don't "fix" it by declaring `LayoutProps` or loosening the tsconfig.
- **Testcontainers missing their startup deadline.** When several worktrees run `go test -tags integration` at the same time, the Postgres and NATS containers can take longer than their wait strategy allows and the package fails with a startup or deadline error before any test logic runs. Rerun that package alone (`go test -count=1 -race -tags integration ./internal/<pkg>/`) before treating it as a code failure. If it still fails alone, it's real.

**How to apply:** check whether the failure names a missing generated type or a container startup timeout before debugging the code. Both are environmental.

Related: [[domain-handler-pattern]]
