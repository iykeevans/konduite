import { Suspense } from "react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { getWorkspace } from "@/lib/api/workspaces";
import { CreateProjectForm } from "../create-project-form";

export default function NewProjectPage({ params }: PageProps<"/workspaces/[workspaceId]/projects/new">) {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 p-8">
      <Suspense fallback={<p className="text-zinc-500">Loading…</p>}>
        {params.then(({ workspaceId }) => (
          <NewProject workspaceId={workspaceId} />
        ))}
      </Suspense>
    </main>
  );
}

async function NewProject({ workspaceId }: { workspaceId: string }) {
  const workspace = await getWorkspace(workspaceId);
  if (!workspace) notFound();

  return (
    <>
      <Link href={`/workspaces/${workspace.id}`} className="text-sm underline">
        {workspace.name}
      </Link>
      <h1 className="text-2xl font-semibold">Create a project</h1>
      <CreateProjectForm workspaceId={workspace.id} />
    </>
  );
}
