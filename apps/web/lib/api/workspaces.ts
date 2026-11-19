import { api } from "./client";

export type WorkspaceRole = "owner" | "admin" | "developer" | "project_manager";

export type Workspace = {
  id: string;
  name: string;
  slug: string;
  role: WorkspaceRole;
  created_by: string;
  created_at: string;
  updated_at: string;
};

// The switcher shows the first 100 workspaces; paging through more is left
// until someone belongs to that many.
export async function listWorkspaces(): Promise<Workspace[]> {
  const res = await api<Workspace[]>("/workspaces?limit=100");
  if (!res.ok) throw new Error(res.error.message);
  return res.data;
}

export async function getWorkspace(id: string): Promise<Workspace | null> {
  const res = await api<Workspace>(`/workspaces/${encodeURIComponent(id)}`);
  if (res.ok) return res.data;
  if (res.status === 404) return null;
  throw new Error(res.error.message);
}

export function createWorkspace(input: { name: string; slug: string }) {
  return api<Workspace>("/workspaces", { method: "POST", body: input });
}
