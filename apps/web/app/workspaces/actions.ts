"use server";

import { redirect } from "next/navigation";
import { createWorkspace } from "@/lib/api/workspaces";

export type CreateWorkspaceState = {
  error?: string;
  fields?: Record<string, string>;
};

export async function createWorkspaceAction(_: CreateWorkspaceState, form: FormData): Promise<CreateWorkspaceState> {
  const res = await createWorkspace({
    name: String(form.get("name") ?? ""),
    slug: String(form.get("slug") ?? ""),
  });
  if (res.ok) redirect(`/workspaces/${res.data.id}`);
  if (res.status === 409) return { error: res.error.message, fields: { slug: res.error.message } };
  return { error: res.error.message, fields: res.error.details?.fields };
}
