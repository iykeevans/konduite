"use client";

import { useActionState } from "react";
import type { Member } from "@/lib/api/members";
import type { WorkspaceRole } from "@/lib/api/workspaces";
import { changeRoleAction, removeMemberAction, type RowState } from "./actions";

const roles: WorkspaceRole[] = ["owner", "admin", "developer", "project_manager"];

export function MemberRow({
  workspaceId,
  member,
  isSelf,
  canManage,
  callerRole,
}: {
  workspaceId: string;
  member: Member;
  isSelf: boolean;
  canManage: boolean;
  callerRole: WorkspaceRole;
}) {
  const [roleState, roleAction, changing] = useActionState<RowState, FormData>(
    changeRoleAction.bind(null, workspaceId, member.user_id),
    {},
  );
  const [removeState, removeAction, removing] = useActionState<RowState>(
    removeMemberAction.bind(null, workspaceId, member.user_id, isSelf),
    {},
  );
  // Only owners can touch an owner or make one; the API enforces this too.
  const editable = canManage && (callerRole === "owner" || member.role !== "owner");
  const choices = callerRole === "owner" ? roles : roles.filter((r) => r !== "owner");
  const error = roleState.error ?? removeState.error;

  return (
    <li className="flex flex-col gap-1 border-b border-zinc-200 py-2 dark:border-zinc-800">
      <div className="flex flex-wrap items-center gap-3">
        <span className="flex-1 truncate">
          {member.email ?? <span className="font-mono text-xs text-zinc-500">{member.user_id}</span>}
          {isSelf && <span className="ml-2 text-xs text-zinc-500">(you)</span>}
        </span>
        {editable ? (
          <form action={roleAction}>
            <select
              name="role"
              defaultValue={member.role}
              disabled={changing}
              onChange={(e) => e.currentTarget.form?.requestSubmit()}
              className="rounded border border-zinc-300 px-2 py-1 text-sm dark:border-zinc-700 dark:bg-zinc-900"
            >
              {choices.map((r) => (
                <option key={r} value={r}>
                  {r.replace("_", " ")}
                </option>
              ))}
            </select>
          </form>
        ) : (
          <span className="text-sm text-zinc-600 dark:text-zinc-400">{member.role.replace("_", " ")}</span>
        )}
        {(editable || isSelf) && (
          <form action={removeAction}>
            <button disabled={removing} className="text-sm text-red-600 disabled:opacity-50">
              {isSelf ? "Leave" : "Remove"}
            </button>
          </form>
        )}
      </div>
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
    </li>
  );
}
