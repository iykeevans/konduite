import { api } from "./client";
import type { WorkspaceRole } from "./workspaces";

export type ProjectRole = "maintainer" | "contributor" | "viewer";

export type ProjectMember = {
  user_id: string;
  role: ProjectRole;
  email: string | null;
  workspace_role: WorkspaceRole;
  joined_at: string;
};

const base = (projectId: string) => `/projects/${encodeURIComponent(projectId)}/members`;

// Shows the first 100 members; paging is left until a project grows that
// large.
export async function listProjectMembers(projectId: string): Promise<ProjectMember[]> {
  const res = await api<ProjectMember[]>(`${base(projectId)}?limit=100`);
  if (!res.ok) throw new Error(res.error.message);
  return res.data;
}

export function addProjectMember(projectId: string, input: { user_id: string; role: string }) {
  return api<ProjectMember>(base(projectId), { method: "POST", body: input });
}

export function changeProjectMemberRole(projectId: string, userId: string, role: string) {
  return api<ProjectMember>(`${base(projectId)}/${encodeURIComponent(userId)}`, { method: "PATCH", body: { role } });
}

export function removeProjectMember(projectId: string, userId: string) {
  return api<null>(`${base(projectId)}/${encodeURIComponent(userId)}`, { method: "DELETE" });
}
