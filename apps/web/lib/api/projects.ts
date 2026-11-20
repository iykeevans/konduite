import { api } from "./client";

export type Project = {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  repository_url: string | null;
  created_by: string;
  created_at: string;
  updated_at: string;
};

export type ProjectInput = { name: string; description: string; repository_url: string };

// Shows the first 100 visible projects; paging is left until a workspace
// has that many.
export async function listProjects(workspaceId: string): Promise<Project[] | null> {
  const res = await api<Project[]>(`/workspaces/${encodeURIComponent(workspaceId)}/projects?limit=100`);
  if (res.ok) return res.data;
  if (res.status === 404) return null;
  throw new Error(res.error.message);
}

export async function getProject(id: string): Promise<Project | null> {
  const res = await api<Project>(`/projects/${encodeURIComponent(id)}`);
  if (res.ok) return res.data;
  if (res.status === 404) return null;
  throw new Error(res.error.message);
}

export function createProject(workspaceId: string, input: ProjectInput) {
  return api<Project>(`/workspaces/${encodeURIComponent(workspaceId)}/projects`, { method: "POST", body: input });
}
