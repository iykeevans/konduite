"use server";

import { refresh } from "next/cache";
import { redirect } from "next/navigation";
import { changeMemberRole, createInvitation, removeMember, revokeInvitation } from "@/lib/api/members";

export type InviteState = {
  token?: string;
  email?: string;
  error?: string;
  fields?: Record<string, string>;
};

export async function inviteAction(workspaceId: string, _: InviteState, form: FormData): Promise<InviteState> {
  const res = await createInvitation(workspaceId, {
    email: String(form.get("email") ?? ""),
    role: String(form.get("role") ?? ""),
  });
  if (!res.ok) return { error: res.error.message, fields: res.error.details?.fields };
  refresh();
  return { token: res.data.token, email: res.data.email };
}

export type RowState = { error?: string };

export async function changeRoleAction(workspaceId: string, userId: string, _: RowState, form: FormData): Promise<RowState> {
  const res = await changeMemberRole(workspaceId, userId, String(form.get("role") ?? ""));
  if (!res.ok) return { error: res.error.message };
  refresh();
  return {};
}

export async function removeMemberAction(workspaceId: string, userId: string, leaving: boolean): Promise<RowState> {
  const res = await removeMember(workspaceId, userId);
  if (!res.ok) return { error: res.error.message };
  if (leaving) redirect("/");
  refresh();
  return {};
}

export async function revokeInvitationAction(workspaceId: string, invitationId: string): Promise<RowState> {
  const res = await revokeInvitation(workspaceId, invitationId);
  if (!res.ok) return { error: res.error.message };
  refresh();
  return {};
}
