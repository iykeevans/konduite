import { Suspense } from "react";
import { CreateWorkspaceForm } from "../create-workspace-form";
import { WorkspaceSwitcher } from "../workspace-switcher";

export default function NewWorkspacePage() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 p-8">
      <Suspense fallback={null}>
        <WorkspaceSwitcher />
      </Suspense>
      <h1 className="text-2xl font-semibold">Create a workspace</h1>
      <CreateWorkspaceForm />
    </main>
  );
}
