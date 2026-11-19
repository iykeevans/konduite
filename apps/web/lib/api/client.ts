import { redirect } from "next/navigation";
import { createClient } from "@/lib/supabase/server";

export type ApiError = {
  code: string;
  message: string;
  details?: { fields?: Record<string, string> };
};

export type ApiResult<T> =
  | { ok: true; status: number; data: T; meta?: { next_cursor?: string; has_more: boolean } }
  | { ok: false; status: number; error: ApiError };

function apiUrl() {
  const url = process.env.KONDUITE_API_URL;
  if (!url) throw new Error("KONDUITE_API_URL must be set (see apps/web/.env.example)");
  return url.replace(/\/$/, "");
}

// Calls the Go API as the signed-in user. Server-only: it reads the session
// cookie and forwards the Supabase access token. getSession (not getUser) is
// enough here because the API verifies the token itself on every request.
// A 401 means the session is dead, so it sends the user to /login.
export async function api<T>(path: string, init: { method?: string; body?: unknown } = {}): Promise<ApiResult<T>> {
  const supabase = await createClient();
  const { data } = await supabase.auth.getSession();
  const token = data.session?.access_token;
  if (!token) redirect("/login");

  const res = await fetch(`${apiUrl()}/api/v1${path}`, {
    method: init.method ?? "GET",
    headers: {
      Authorization: `Bearer ${token}`,
      ...(init.body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
    cache: "no-store",
  });
  if (res.status === 401) redirect("/login");

  const body = await res.json().catch(() => null);
  if (!res.ok) {
    return {
      ok: false,
      status: res.status,
      error: body?.error ?? { code: "internal", message: `API request failed with status ${res.status}` },
    };
  }
  return { ok: true, status: res.status, data: body.data as T, meta: body.meta };
}
