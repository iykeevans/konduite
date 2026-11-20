"use client";

import { useActionState } from "react";
import type { Invitation } from "@/lib/api/members";
import { revokeInvitationAction, type RowState } from "./actions";

export function InvitationRow({ workspaceId, invitation, expired }: { workspaceId: string; invitation: Invitation; expired: boolean }) {
  const [state, action, pending] = useActionState<RowState>(revokeInvitationAction.bind(null, workspaceId, invitation.id), {});

  return (
    <li className="flex flex-col gap-1 border-b border-zinc-200 py-2 dark:border-zinc-800">
      <div className="flex flex-wrap items-center gap-3">
        <span className="flex-1 truncate">{invitation.email}</span>
        <span className="text-sm text-zinc-600 dark:text-zinc-400">{invitation.role.replace("_", " ")}</span>
        <span className="text-xs text-zinc-500">{expired ? "expired" : `expires ${invitation.expires_at.slice(0, 10)}`}</span>
        <form action={action}>
          <button disabled={pending} className="text-sm text-red-600 disabled:opacity-50">
            Revoke
          </button>
        </form>
      </div>
      {state.error && (
        <p role="alert" className="text-sm text-red-600">
          {state.error}
        </p>
      )}
    </li>
  );
}
