"use client";

import { useActionState } from "react";
import type { ProjectMember, ProjectRole } from "@/lib/api/project-members";
import { changeRoleAction, removeMemberAction, type RowState } from "./actions";

const projectRoles: ProjectRole[] = ["maintainer", "contributor", "viewer"];

export function ProjectMemberRow({
  workspaceId,
  projectId,
  member,
  isSelf,
  canManage,
}: {
  workspaceId: string;
  projectId: string;
  member: ProjectMember;
  isSelf: boolean;
  canManage: boolean;
}) {
  const [roleState, roleAction, changing] = useActionState<RowState, FormData>(
    changeRoleAction.bind(null, projectId, member.user_id),
    {},
  );
  const [removeState, removeAction, removing] = useActionState<RowState>(
    removeMemberAction.bind(null, workspaceId, projectId, member.user_id, isSelf),
    {},
  );
  const error = roleState.error ?? removeState.error;

  return (
    <li className="flex flex-col gap-1 border-b border-zinc-200 py-2 dark:border-zinc-800">
      <div className="flex flex-wrap items-center gap-3">
        <span className="flex-1 truncate">
          {member.email ?? <span className="font-mono text-xs text-zinc-500">{member.user_id}</span>}
          {isSelf && <span className="ml-2 text-xs text-zinc-500">(you)</span>}
        </span>
        {canManage ? (
          <form action={roleAction}>
            <select
              name="role"
              aria-label="Project role"
              defaultValue={member.role}
              disabled={changing}
              onChange={(e) => e.currentTarget.form?.requestSubmit()}
              className="rounded border border-zinc-300 px-2 py-1 text-sm dark:border-zinc-700 dark:bg-zinc-900"
            >
              {projectRoles.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
          </form>
        ) : (
          <span className="text-sm text-zinc-600 dark:text-zinc-400">{member.role}</span>
        )}
        {(canManage || isSelf) && (
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
