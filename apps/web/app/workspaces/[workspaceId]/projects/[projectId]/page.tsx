import { Suspense } from "react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { listMembers } from "@/lib/api/members";
import { listProjectMembers } from "@/lib/api/project-members";
import { getProject } from "@/lib/api/projects";
import { getWorkspace } from "@/lib/api/workspaces";
import { createClient } from "@/lib/supabase/server";
import { AddMemberForm } from "./add-member-form";
import { ProjectMemberRow } from "./member-row";

export default function ProjectPage({ params }: PageProps<"/workspaces/[workspaceId]/projects/[projectId]">) {
  return (
    <main className="flex flex-1 flex-col items-center gap-8 p-8">
      <Suspense fallback={<p className="text-zinc-500">Loading…</p>}>
        {params.then(({ workspaceId, projectId }) => (
          <ProjectDetails workspaceId={workspaceId} id={projectId} />
        ))}
      </Suspense>
    </main>
  );
}

async function ProjectDetails({ workspaceId, id }: { workspaceId: string; id: string }) {
  const project = await getProject(id);
  if (!project || project.workspace_id !== workspaceId) notFound();

  return (
    <div className="flex w-full max-w-2xl flex-col gap-8">
      <section className="flex flex-col items-center gap-2 text-center">
        <Link href={`/workspaces/${workspaceId}`} className="text-sm underline">
          Back to workspace
        </Link>
        <h1 className="text-2xl font-semibold">{project.name}</h1>
        {project.description && <p className="text-zinc-600 dark:text-zinc-400">{project.description}</p>}
        {project.repository_url && <p className="font-mono text-sm text-zinc-500">{project.repository_url}</p>}
      </section>
      <Suspense fallback={<p className="text-zinc-500">Loading members…</p>}>
        <ProjectMembers workspaceId={workspaceId} projectId={id} />
      </Suspense>
    </div>
  );
}

async function ProjectMembers({ workspaceId, projectId }: { workspaceId: string; projectId: string }) {
  const [workspace, members, claims] = await Promise.all([
    getWorkspace(workspaceId),
    listProjectMembers(projectId),
    createClient().then((s) => s.auth.getClaims()),
  ]);
  if (!workspace) notFound();
  const selfId = claims.data?.claims.sub;
  // Mirrors the API's rule only to decide which controls to show; the API
  // enforces it.
  const canManage =
    workspace.role === "owner" ||
    workspace.role === "admin" ||
    members.some((m) => m.user_id === selfId && m.role === "maintainer");

  const inProject = new Set(members.map((m) => m.user_id));
  const candidates = canManage
    ? (await listMembers(workspaceId))
        .filter((m) => !inProject.has(m.user_id))
        .map((m) => ({ user_id: m.user_id, label: m.email ?? m.user_id }))
    : [];

  return (
    <>
      {canManage && (
        <section className="flex flex-col gap-2">
          <h2 className="text-lg font-medium">Add a member</h2>
          <AddMemberForm projectId={projectId} candidates={candidates} />
        </section>
      )}
      <section className="flex flex-col gap-2">
        <h2 className="text-lg font-medium">Members</h2>
        {members.length === 0 ? (
          <p className="text-sm text-zinc-500">No one has been added yet. Workspace owners and admins maintain every project.</p>
        ) : (
          <ul>
            {members.map((m) => (
              <ProjectMemberRow
                key={m.user_id}
                workspaceId={workspaceId}
                projectId={projectId}
                member={m}
                isSelf={m.user_id === selfId}
                canManage={canManage}
              />
            ))}
          </ul>
        )}
      </section>
    </>
  );
}
