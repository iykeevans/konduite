"use server";

import { redirect } from "next/navigation";
import { createProject } from "@/lib/api/projects";

export type CreateProjectState = {
  error?: string;
  fields?: Record<string, string>;
};

export async function createProjectAction(
  workspaceId: string,
  _: CreateProjectState,
  form: FormData,
): Promise<CreateProjectState> {
  const res = await createProject(workspaceId, {
    name: String(form.get("name") ?? ""),
    description: String(form.get("description") ?? ""),
    repository_url: String(form.get("repository_url") ?? ""),
  });
  if (res.ok) redirect(`/workspaces/${workspaceId}/projects/${res.data.id}`);
  if (res.status === 409) return { error: res.error.message, fields: { name: res.error.message } };
  return { error: res.error.message, fields: res.error.details?.fields };
}
