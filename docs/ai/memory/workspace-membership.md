---
name: workspace-membership
description: "How invitations, role changes and removals work and why (token handling, email binding, last-owner guard): read before touching backend/internal/members or anything that adds users to a workspace"
metadata:
  type: project
  modified: 2026-11-20
---

# Workspace membership and invitations

Decided in PLAT-008 (#18), 2026-11-20. Code: `backend/internal/members`.

- **Invitation tokens:** `kdi_` + 32 random bytes, base64url. Only the hex SHA-256 is stored (`invitations.token_hash`). The raw token is returned once in the create response (`Cache-Control: no-store`) and never listed or logged. 7-day expiry, single use, revocable.
- **No email provider yet:** the inviter copies the accept link. The web link is `/invitations/accept#<token>`: the fragment never reaches a server, and the page POSTs the token in a body to `POST /api/v1/invitations/accept`. Never put the token in an API path or query.
- **Email binding:** `auth.Principal.Email` is the lowercased `email` claim of the verified Supabase token. Accept requires it to equal the invitation email (else 403). It only proves the user owns the address when Supabase requires email confirmation, which local dev turns off; the token is the real secret.
- **`workspace_members.email`** (00008) is the email at join time, set on accept and on workspace create. It powers the "invite an existing member → 409" check and the members list. Null for older rows; not synced if the user changes their Supabase email.
- **Guard rails** live in `CheckRoleChange` / `CheckRemoval` (pure, table-tested). The repository locks every membership row of the workspace (`LockMembers ... FOR UPDATE`) and calls them inside the write tx, so two owners demoting each other can't both win. Rules: last owner can't be demoted, removed or leave (409); only owners touch owners or make owners (403); any member may remove themselves (leave).
- **Audit actions:** `invitation.created`, `invitation.revoked`, `member.joined` (actor = the new member), `member.role_changed` (`{user_id, from, to}`, none for a no-op), `member.removed` (`{user_id, role, self}`).
- **Accept has no `authz.Can`:** the token is the grant. Unknown, used, revoked and expired tokens all get the same 404.

Related: [[workspace-authorization]], [[domain-handler-pattern]], [[user-ids-have-no-auth-fk]]
