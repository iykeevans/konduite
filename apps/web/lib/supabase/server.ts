import { createServerClient } from "@supabase/ssr";
import { cookies } from "next/headers";
import { connection } from "next/server";
import { supabaseEnv } from "./env";

export async function createClient() {
  // supabase-js reads the clock (Date.now) to check token expiry. Without this,
  // Cache Components may run it during a runtime prefetch and reject the render.
  await connection();
  const cookieStore = await cookies();
  const { url, key } = supabaseEnv();
  return createServerClient(url, key, {
    cookies: {
      getAll() {
        return cookieStore.getAll();
      },
      setAll(cookiesToSet) {
        try {
          for (const { name, value, options } of cookiesToSet) {
            cookieStore.set(name, value, options);
          }
        } catch {
          // Server Components cannot set cookies. proxy.ts refreshes the
          // session on every navigation, so dropping the write here is safe.
        }
      },
    },
  });
}
