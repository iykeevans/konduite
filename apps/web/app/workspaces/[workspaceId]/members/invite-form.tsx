"use client";

import { useActionState, useState } from "react";
import { inviteAction, type InviteState } from "./actions";

const inputClass = "rounded border border-zinc-300 px-3 py-2 dark:border-zinc-700 dark:bg-zinc-900";

export function InviteForm({ workspaceId }: { workspaceId: string }) {
  const [state, action, pending] = useActionState<InviteState, FormData>(inviteAction.bind(null, workspaceId), {});
  const [copied, setCopied] = useState(false);
  // The token is in the fragment, which browsers never send to a server.
  const link = state.token ? `${window.location.origin}/invitations/accept#${state.token}` : "";

  return (
    <div className="flex flex-col gap-3">
      <form action={action} onSubmit={() => setCopied(false)} className="flex flex-wrap items-end gap-2">
        <label className="flex flex-1 flex-col gap-1 text-sm">
          Email
          <input name="email" type="email" required maxLength={254} className={inputClass} />
          {state.fields?.email && <span className="text-red-600">{state.fields.email}</span>}
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Role
          <select name="role" defaultValue="developer" className={inputClass}>
            <option value="developer">developer</option>
            <option value="project_manager">project manager</option>
            <option value="admin">admin</option>
          </select>
        </label>
        <button disabled={pending} className="rounded bg-zinc-900 px-4 py-2 text-white disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900">
          Invite
        </button>
      </form>
      {state.error && !state.fields && (
        <p role="alert" className="text-sm text-red-600">
          {state.error}
        </p>
      )}
      {link && (
        <div className="flex flex-col gap-2 rounded border border-zinc-300 p-3 text-sm dark:border-zinc-700">
          <p>
            Konduite does not send email yet. Send this link to <strong>{state.email}</strong> yourself. It works once,
            for that address only, and expires in 7 days. It is shown only now.
          </p>
          <div className="flex gap-2">
            <input readOnly value={link} className={`${inputClass} flex-1 font-mono text-xs`} onFocus={(e) => e.target.select()} />
            <button
              type="button"
              onClick={() => navigator.clipboard.writeText(link).then(() => setCopied(true))}
              className="rounded border border-zinc-300 px-3 py-2 dark:border-zinc-700"
            >
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
