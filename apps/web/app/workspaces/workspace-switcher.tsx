import Link from "next/link";
import { listWorkspaces } from "@/lib/api/workspaces";
import { SwitcherSelect } from "./switcher-select";

export async function WorkspaceSwitcher({ currentId }: { currentId?: string }) {
  const workspaces = await listWorkspaces();
  if (workspaces.length === 0) {
    return (
      <Link href="/workspaces/new" className="text-sm underline">
        Create your first workspace
      </Link>
    );
  }
  return (
    <SwitcherSelect
      currentId={currentId}
      options={workspaces.map((w) => ({ id: w.id, name: w.name, slug: w.slug }))}
    />
  );
}
