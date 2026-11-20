import { Suspense } from "react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { getProject } from "@/lib/api/projects";

export default function ProjectPage({ params }: PageProps<"/workspaces/[workspaceId]/projects/[projectId]">) {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 p-8">
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
    <section className="flex max-w-xl flex-col items-center gap-2 text-center">
      <Link href={`/workspaces/${workspaceId}`} className="text-sm underline">
        Back to workspace
      </Link>
      <h1 className="text-2xl font-semibold">{project.name}</h1>
      {project.description && <p className="text-zinc-600 dark:text-zinc-400">{project.description}</p>}
      {project.repository_url && <p className="font-mono text-sm text-zinc-500">{project.repository_url}</p>}
    </section>
  );
}
