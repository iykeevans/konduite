import { Suspense } from "react";
import { redirect } from "next/navigation";
import { createClient } from "@/lib/supabase/server";
import { signOut } from "./login/actions";
import { WorkspaceSwitcher } from "./workspaces/workspace-switcher";

export default function Home() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 p-8">
      <h1 className="text-2xl font-semibold">Konduite</h1>
      <Suspense fallback={<p className="text-zinc-500">Loading…</p>}>
        <CurrentUser />
      </Suspense>
    </main>
  );
}

async function CurrentUser() {
  const supabase = await createClient();
  const { data } = await supabase.auth.getClaims();
  if (!data?.claims) redirect("/login");

  return (
    <>
      <p className="text-zinc-600 dark:text-zinc-400">Signed in as {String(data.claims.email ?? data.claims.sub)}</p>
      <WorkspaceSwitcher />
      <form action={signOut}>
        <button className="rounded border border-zinc-300 px-4 py-2 dark:border-zinc-700">Sign out</button>
      </form>
    </>
  );
}
