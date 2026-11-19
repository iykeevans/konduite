"use client";

import { useActionState } from "react";
import { signIn, signUp, type AuthState } from "./actions";

export function LoginForm() {
  const [signInState, signInAction, signingIn] = useActionState<AuthState, FormData>(signIn, {});
  const [signUpState, signUpAction, signingUp] = useActionState<AuthState, FormData>(signUp, {});
  const error = signInState.error ?? signUpState.error;
  const busy = signingIn || signingUp;

  return (
    <form className="flex w-full max-w-sm flex-col gap-4">
      <label className="flex flex-col gap-1 text-sm">
        Email
        <input name="email" type="email" autoComplete="email" required className="rounded border border-zinc-300 px-3 py-2 dark:border-zinc-700 dark:bg-zinc-900" />
      </label>
      <label className="flex flex-col gap-1 text-sm">
        Password
        <input name="password" type="password" autoComplete="current-password" required minLength={6} className="rounded border border-zinc-300 px-3 py-2 dark:border-zinc-700 dark:bg-zinc-900" />
      </label>
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <button formAction={signInAction} disabled={busy} className="flex-1 rounded bg-zinc-900 px-4 py-2 text-white disabled:opacity-50 dark:bg-zinc-100 dark:text-zinc-900">
          Sign in
        </button>
        <button formAction={signUpAction} disabled={busy} className="flex-1 rounded border border-zinc-300 px-4 py-2 disabled:opacity-50 dark:border-zinc-700">
          Create account
        </button>
      </div>
    </form>
  );
}
