// The only way the browser reaches the backend: this Worker forwards /api/* to API_ORIGIN (api-site.beeloy.com),
// streaming bodies in both directions (needed for SSE) and adding the secret + real client IP for the Go API.
import { buildTarget, validIp } from "@/lib/proxyTarget";

export const dynamic = "force-dynamic";

const REQUEST_HEADERS = ["cookie", "content-type", "accept", "accept-language", "origin", "if-none-match", "last-event-id", "x-requested-with", "x-request-id"];
const RESPONSE_HEADERS = ["content-type", "cache-control", "etag", "retry-after", "set-cookie", "location", "content-disposition", "x-i18n-fallback", "x-request-id"];

async function handle(req: Request, ctx: { params: Promise<{ path: string[] }> }): Promise<Response> {
  const { path } = await ctx.params;
  const origin = process.env.API_ORIGIN ?? "http://localhost:8080";
  const target = buildTarget(origin, path, new URL(req.url).search);
  if (!target) return new Response("not found", { status: 404 });

  const headers = new Headers();
  for (const h of REQUEST_HEADERS) {
    const v = req.headers.get(h);
    if (v) headers.set(h, v);
  }
  const secret = process.env.PROXY_SHARED_SECRET;
  if (secret) headers.set("X-Proxy-Secret", secret);
  const ip = validIp(req.headers.get("cf-connecting-ip")); // never a client-controlled X-Forwarded-For
  if (ip) headers.set("X-Real-Client-IP", ip);

  const hasBody = !["GET", "HEAD"].includes(req.method);
  const init: RequestInit & { duplex?: "half" } = { method: req.method, headers, redirect: "manual", signal: req.signal };
  if (hasBody) {
    init.body = req.body;
    init.duplex = "half";
  }
  let upstream: Response;
  try {
    upstream = await fetch(target, init);
  } catch {
    return Response.json({ error: { code: "unavailable", message: "upstream unavailable" } }, { status: 502 });
  }
  const out = new Headers();
  for (const h of RESPONSE_HEADERS) {
    const values = h === "set-cookie" ? upstream.headers.getSetCookie() : [upstream.headers.get(h)].filter(Boolean) as string[];
    for (const v of values) out.append(h, v);
  }
  if ((upstream.headers.get("content-type") ?? "").startsWith("text/event-stream")) out.set("Cache-Control", "private, no-store, no-transform");
  return new Response(upstream.status === 204 || upstream.status === 304 ? null : upstream.body, { status: upstream.status, headers: out });
}

export { handle as GET, handle as POST, handle as PUT, handle as PATCH, handle as DELETE };
