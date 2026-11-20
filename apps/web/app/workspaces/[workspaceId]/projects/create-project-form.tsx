"use client";

import { useActionState } from "react";
import { createProjectAction, type CreateProjectState } from "./actions";

const inputClass = "rounded border border-zinc-300 px-3 py-2 dark:border-zinc-700 dark:bg-zinc-900";

export function CreateProjectForm({ workspaceId }: { workspaceId: string }) {
  const [state, action, pending] = useActionState<CreateProjectState, FormData>(
    createProjectAction.bind(null, workspaceId),
    {},
  );

  return (
    <form action={action} className="flex w-full max-w-sm flex-col gap-4">
      <label className="flex flex-col gap-1 text-sm">
        Name
        <input name="name" required maxLength={100} className={inputClass} />
        {state.fields?.name && <span className="text-red-600">{state.fields.name}</span>}
      </label>
      <label className="flex flex-col gap-1 text-sm">
        Description
        <textarea name="description" maxLength={2000} rows={3} className={inputClass} />
        {state.fields?.description && <span className="text-red-600">{state.fields.description}</span>}
      </label>
      <label className="flex flex-col gap-1 text-sm">
        Repository URL
        <input
          name="repository_url"
          maxLength={500}
          placeholder="git@github.com:org/repo.git"
          className={`${inputClass} font-mono`}
        />
        <span className="text-zinc-500">https or ssh, without credentials. Optional.</span>
        {state.fields?.repository_url && <span className="text-red-600">{state.fields.repository_url}</span>}
      </label>
      {state.error && !state.fields && (
        <p role="alert" className="text-sm text-red-600">
          {state.error}
        </p>
      )}
      <button disabled={pending} className="rounded bg-zinc-900 px-4 py-2 text-white disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900">
        Create project
      </button>
    </form>
  );
}
