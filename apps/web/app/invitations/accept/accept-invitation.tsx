"use client";

import { useState, useSyncExternalStore, useTransition } from "react";
import { acceptInvitationAction } from "./actions";

const subscribe = () => () => {};

export function AcceptInvitation() {
  // The token arrives in the URL fragment, which never reaches a server.
  // Read once on the client, then dropped from the address bar.
  const token = useSyncExternalStore(
    subscribe,
    () => readToken(),
    () => null,
  );
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();

  if (token === null) return <p className="text-zinc-500">Loading…</p>;
  if (token === "") {
    return <p className="text-zinc-600 dark:text-zinc-400">This link has no invitation in it. Ask whoever invited you for the full link.</p>;
  }

  return (
    <div className="flex flex-col items-center gap-4">
      <p className="text-zinc-600 dark:text-zinc-400">You have been invited to a Konduite workspace.</p>
      <button
        disabled={pending}
        onClick={() =>
          startTransition(async () => {
            const res = await acceptInvitationAction(token);
            if (res.error) setError(res.error);
          })
        }
        className="rounded bg-zinc-900 px-4 py-2 text-white disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900"
      >
        Join workspace
      </button>
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
    </div>
  );
}

let cached: string | undefined;

function readToken(): string {
  if (cached === undefined) {
    cached = decodeURIComponent(window.location.hash.slice(1));
    if (cached) history.replaceState(null, "", window.location.pathname);
  }
  return cached;
}
