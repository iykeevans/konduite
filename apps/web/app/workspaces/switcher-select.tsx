"use client";

import { useRouter } from "next/navigation";

const NEW = "__new__";

export function SwitcherSelect({
  currentId,
  options,
}: {
  currentId?: string;
  options: { id: string; name: string; slug: string }[];
}) {
  const router = useRouter();
  return (
    <label className="flex items-center gap-2 text-sm">
      <span className="text-zinc-500">Workspace</span>
      <select
        value={currentId ?? ""}
        onChange={(e) => router.push(e.target.value === NEW ? "/workspaces/new" : `/workspaces/${e.target.value}`)}
        className="rounded border border-zinc-300 px-2 py-1 dark:border-zinc-700 dark:bg-zinc-900"
      >
        {!currentId && (
          <option value="" disabled>
            Choose a workspace…
          </option>
        )}
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name} ({o.slug})
          </option>
        ))}
        <option value={NEW}>+ New workspace</option>
      </select>
    </label>
  );
}
