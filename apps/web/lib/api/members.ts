import { api } from "./client";
import type { WorkspaceRole } from "./workspaces";

export type InvitableRole = Exclude<WorkspaceRole, "owner">;

export type Member = {
  user_id: string;
  role: WorkspaceRole;
  email: string | null;
  joined_at: string;
};

export type Invitation = {
  id: string;
  workspace_id: string;
  email: string;
  role: InvitableRole;
  invited_by: string;
  expires_at: string;
  created_at: string;
};

const base = (workspaceId: string) => `/workspaces/${encodeURIComponent(workspaceId)}`;

// Both lists show the first 100 entries; paging is left until a workspace
// grows that large.
export async function listMembers(workspaceId: string): Promise<Member[]> {
  const res = await api<Member[]>(`${base(workspaceId)}/members?limit=100`);
  if (!res.ok) throw new Error(res.error.message);
  return res.data;
}

export async function listInvitations(workspaceId: string): Promise<Invitation[]> {
  const res = await api<Invitation[]>(`${base(workspaceId)}/invitations?limit=100`);
  if (!res.ok) throw new Error(res.error.message);
  return res.data;
}

export function createInvitation(workspaceId: string, input: { email: string; role: string }) {
  return api<Invitation & { token: string }>(`${base(workspaceId)}/invitations`, { method: "POST", body: input });
}

export function revokeInvitation(workspaceId: string, invitationId: string) {
  return api<null>(`${base(workspaceId)}/invitations/${encodeURIComponent(invitationId)}`, { method: "DELETE" });
}

export function changeMemberRole(workspaceId: string, userId: string, role: string) {
  return api<Member>(`${base(workspaceId)}/members/${encodeURIComponent(userId)}`, { method: "PATCH", body: { role } });
}

export function removeMember(workspaceId: string, userId: string) {
  return api<null>(`${base(workspaceId)}/members/${encodeURIComponent(userId)}`, { method: "DELETE" });
}

// The token goes in the request body so it never appears in a URL or an
// access log.
export function acceptInvitation(token: string) {
  return api<{ workspace_id: string; role: WorkspaceRole }>("/invitations/accept", { method: "POST", body: { token } });
}
