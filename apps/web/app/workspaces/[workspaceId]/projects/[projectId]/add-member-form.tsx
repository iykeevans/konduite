"use client";

import { useActionState } from "react";
import { addMemberAction, type AddMemberState } from "./actions";

const inputClass = "rounded border border-zinc-300 px-3 py-2 dark:border-zinc-700 dark:bg-zinc-900";

export function AddMemberForm({
  projectId,
  candidates,
}: {
  projectId: string;
  candidates: { user_id: string; label: string }[];
}) {
  const [state, action, pending] = useActionState<AddMemberState, FormData>(addMemberAction.bind(null, projectId), {});

  if (candidates.length === 0) {
    return <p className="text-sm text-zinc-500">Everyone in the workspace is already in this project.</p>;
  }

  return (
    <div className="flex flex-col gap-2">
      <form action={action} className="flex flex-wrap items-end gap-2">
        <label className="flex flex-1 flex-col gap-1 text-sm">
          Workspace member
          <select name="user_id" required className={inputClass}>
            {candidates.map((c) => (
              <option key={c.user_id} value={c.user_id}>
                {c.label}
              </option>
            ))}
          </select>
          {state.fields?.user_id && <span className="text-red-600">{state.fields.user_id}</span>}
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Role
          <select name="role" defaultValue="contributor" className={inputClass}>
            <option value="maintainer">maintainer</option>
            <option value="contributor">contributor</option>
            <option value="viewer">viewer</option>
          </select>
        </label>
        <button
          disabled={pending}
          className="rounded bg-zinc-900 px-4 py-2 text-white disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900"
        >
          Add
        </button>
      </form>
      {state.error && !state.fields && (
        <p role="alert" className="text-sm text-red-600">
          {state.error}
        </p>
      )}
    </div>
  );
}
