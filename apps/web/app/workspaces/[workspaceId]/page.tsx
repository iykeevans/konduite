import { Suspense } from "react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { getWorkspace } from "@/lib/api/workspaces";
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
      <Link href={`/workspaces/${workspace.id}/members`} className="text-sm underline">
        Members
      </Link>
    </section>
  );
}
