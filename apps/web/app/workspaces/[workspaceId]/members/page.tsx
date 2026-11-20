import { Suspense } from "react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { listInvitations, listMembers, type Invitation } from "@/lib/api/members";
import { getWorkspace } from "@/lib/api/workspaces";
import { createClient } from "@/lib/supabase/server";
import { InvitationRow } from "./invitation-row";
import { InviteForm } from "./invite-form";
import { MemberRow } from "./member-row";

export default function MembersPage({ params }: PageProps<"/workspaces/[workspaceId]/members">) {
  return (
    <main className="flex flex-1 flex-col items-center gap-6 p-8">
      <Suspense fallback={<p className="text-zinc-500">Loading…</p>}>
        {params.then(({ workspaceId }) => (
          <Members workspaceId={workspaceId} />
        ))}
      </Suspense>
    </main>
  );
}

function isExpired(inv: Invitation) {
  return new Date(inv.expires_at).getTime() <= Date.now();
}

async function Members({ workspaceId }: { workspaceId: string }) {
  const workspace = await getWorkspace(workspaceId);
  if (!workspace) notFound();
  const canManage = workspace.role === "owner" || workspace.role === "admin";
  const [members, invitations, claims] = await Promise.all([
    listMembers(workspaceId),
    canManage ? listInvitations(workspaceId) : Promise.resolve([]),
    createClient().then((s) => s.auth.getClaims()),
  ]);
  const selfId = claims.data?.claims.sub;

  return (
    <div className="flex w-full max-w-2xl flex-col gap-8">
      <header className="flex items-baseline justify-between">
        <h1 className="text-2xl font-semibold">{workspace.name} members</h1>
        <Link href={`/workspaces/${workspaceId}`} className="text-sm text-zinc-500 underline">
          Back to workspace
        </Link>
      </header>

      {canManage && (
        <section className="flex flex-col gap-2">
          <h2 className="text-lg font-medium">Invite someone</h2>
          <InviteForm workspaceId={workspaceId} />
        </section>
      )}

      <section className="flex flex-col gap-2">
        <h2 className="text-lg font-medium">Members</h2>
        <ul>
          {members.map((m) => (
            <MemberRow
              key={m.user_id}
              workspaceId={workspaceId}
              member={m}
              isSelf={m.user_id === selfId}
              canManage={canManage}
              callerRole={workspace.role}
            />
          ))}
        </ul>
      </section>

      {canManage && invitations.length > 0 && (
        <section className="flex flex-col gap-2">
          <h2 className="text-lg font-medium">Pending invitations</h2>
          <ul>
            {invitations.map((inv) => (
              <InvitationRow key={inv.id} workspaceId={workspaceId} invitation={inv} expired={isExpired(inv)} />
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}
