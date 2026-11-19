"use client";

import { useActionState, useState } from "react";
import { createWorkspaceAction, type CreateWorkspaceState } from "./actions";

function slugify(name: string) {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 50)
    .replace(/-+$/, "");
}

const inputClass = "rounded border border-zinc-300 px-3 py-2 dark:border-zinc-700 dark:bg-zinc-900";

export function CreateWorkspaceForm() {
  const [state, action, pending] = useActionState<CreateWorkspaceState, FormData>(createWorkspaceAction, {});
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);

  return (
    <form action={action} className="flex w-full max-w-sm flex-col gap-4">
      <label className="flex flex-col gap-1 text-sm">
        Name
        <input
          name="name"
          required
          maxLength={100}
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            if (!slugEdited) setSlug(slugify(e.target.value));
          }}
          className={inputClass}
        />
        {state.fields?.name && <span className="text-red-600">{state.fields.name}</span>}
      </label>
      <label className="flex flex-col gap-1 text-sm">
        Slug
        <input
          name="slug"
          required
          minLength={3}
          maxLength={50}
          pattern="[a-z0-9]+(-[a-z0-9]+)*"
          title="Lowercase letters, digits and single hyphens"
          value={slug}
          onChange={(e) => {
            setSlug(e.target.value);
            setSlugEdited(true);
          }}
          className={`${inputClass} font-mono`}
        />
        {state.fields?.slug && <span className="text-red-600">{state.fields.slug}</span>}
      </label>
      {state.error && !state.fields && (
        <p role="alert" className="text-sm text-red-600">
          {state.error}
        </p>
      )}
      <button disabled={pending} className="rounded bg-zinc-900 px-4 py-2 text-white disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900">
        Create workspace
      </button>
    </form>
  );
}
