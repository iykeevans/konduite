import { Suspense } from "react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { listProjects } from "@/lib/api/projects";
import { getWorkspace, type WorkspaceRole } from "@/lib/api/workspaces";
import { WorkspaceSwitcher } from "../workspace-switcher";

export default function WorkspacePage({ params }: PageProps<"/workspaces/[workspaceId]">) {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 p-8">
      <Suspense fallback={<p className="text-zinc-500">Loading…</p>}>
        {params.then(({ workspaceId }) => (
          <>
            <WorkspaceSwitcher currentId={workspaceId} />
            <WorkspaceDetails id={workspaceId} />
          </>
        ))}
      </Suspense>
    </main>
  );
}

async function WorkspaceDetails({ id }: { id: string }) {
  const workspace = await getWorkspace(id);
  if (!workspace) notFound();

  return (
    <section className="flex flex-col items-center gap-2">
      <h1 className="text-2xl font-semibold">{workspace.name}</h1>
      <p className="font-mono text-sm text-zinc-500">{workspace.slug}</p>
      <p className="text-sm text-zinc-600 dark:text-zinc-400">Your role: {workspace.role.replace("_", " ")}</p>
      <Projects workspaceId={workspace.id} role={workspace.role} />
    </section>
  );
}

// Mirrors the API's rule only to hide a button the API would refuse.
const canCreateProjects: WorkspaceRole[] = ["owner", "admin", "project_manager"];

async function Projects({ workspaceId, role }: { workspaceId: string; role: WorkspaceRole }) {
  const projects = (await listProjects(workspaceId)) ?? [];

  return (
    <div className="mt-6 flex w-full max-w-sm flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Projects</h2>
        {canCreateProjects.includes(role) && (
          <Link href={`/workspaces/${workspaceId}/projects/new`} className="text-sm underline">
            New project
          </Link>
        )}
      </div>
      {projects.length === 0 ? (
        <p className="text-sm text-zinc-500">No projects you can see yet.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {projects.map((p) => (
            <li key={p.id}>
              <Link href={`/workspaces/${workspaceId}/projects/${p.id}`} className="underline">
                {p.name}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
