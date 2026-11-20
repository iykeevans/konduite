"use server";

import { redirect } from "next/navigation";
import { acceptInvitation } from "@/lib/api/members";

export type AcceptState = { error?: string };

export async function acceptInvitationAction(token: string): Promise<AcceptState> {
  const res = await acceptInvitation(token);
  if (!res.ok) return { error: res.error.message };
  redirect(`/workspaces/${res.data.workspace_id}`);
}
