import { AcceptInvitation } from "./accept-invitation";

// Signed-out visitors are sent to /login by proxy.ts. They sign in (or sign
// up) with the invited email, then open the link again.
export default function AcceptInvitationPage() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-6 p-8">
      <h1 className="text-2xl font-semibold">Join a workspace</h1>
      <AcceptInvitation />
    </main>
  );
}
