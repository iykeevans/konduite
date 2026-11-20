"use server";

import { refresh } from "next/cache";
import { redirect } from "next/navigation";
import { addProjectMember, changeProjectMemberRole, removeProjectMember } from "@/lib/api/project-members";

export type AddMemberState = { error?: string; fields?: Record<string, string> };

export async function addMemberAction(projectId: string, _: AddMemberState, form: FormData): Promise<AddMemberState> {
  const res = await addProjectMember(projectId, {
    user_id: String(form.get("user_id") ?? ""),
    role: String(form.get("role") ?? ""),
  });
  if (!res.ok) return { error: res.error.message, fields: res.error.details?.fields };
  refresh();
  return {};
}

export type RowState = { error?: string };

export async function changeRoleAction(projectId: string, userId: string, _: RowState, form: FormData): Promise<RowState> {
  const res = await changeProjectMemberRole(projectId, userId, String(form.get("role") ?? ""));
  if (!res.ok) return { error: res.error.message };
  refresh();
  return {};
}

// Someone who leaves may no longer see the project, so they go back to the
// workspace.
export async function removeMemberAction(
  workspaceId: string,
  projectId: string,
  userId: string,
  leaving: boolean,
): Promise<RowState> {
  const res = await removeProjectMember(projectId, userId);
  if (!res.ok) return { error: res.error.message };
  if (leaving) redirect(`/workspaces/${encodeURIComponent(workspaceId)}`);
  refresh();
  return {};
}
