import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { vi } from "vitest";
import fallback from "@/contracts/fallback-en.json";
import { Providers } from "@/components/Providers";

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response> | undefined;

export function json(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });
}

/** Replaces fetch: handlers are tried in order; the first non-undefined answer wins; /me answers 401 by default. */
export function mockFetch(...handlers: Handler[]) {
  const calls: { url: string; init?: RequestInit }[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
    calls.push({ url, init });
    for (const h of handlers) {
      const r = await h(url, init);
      if (r) return r;
    }
    if (url.includes("/api/v1/me") && !url.includes("/me/")) return json({ error: { code: "unauthorized", message: "x" } }, 401);
    return json({ error: { code: "not_found", message: "x" } }, 404);
  });
  vi.stubGlobal("fetch", fn);
  return { fn, calls };
}

export function renderApp(ui: ReactElement, locale = "en", messages: Record<string, string> = fallback as Record<string, string>) {
  return render(<Providers locale={locale} messages={messages}>{ui}</Providers>);
}

export const me = { id: "u1", nickname: "Alice", bio: "", avatar_url: null, slug: "alice", locale: "en", status: "active", profile_public: true, roles: [], created_at: "2026-01-01T00:00:00Z" };
