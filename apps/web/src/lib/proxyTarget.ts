/** URL on the API origin for a proxied /api/** request, or null if the path must not be forwarded. */
export function buildTarget(origin: string, path: string[], search: string): string | null {
  const joined = "/api/" + path.join("/");
  if (!joined.startsWith("/api/v1/") || joined.includes("..")) return null;
  return origin.replace(/\/$/, "") + joined + search;
}

const IPV4 = /^(\d{1,3})(\.\d{1,3}){3}$/;
const IPV6 = /^[0-9a-f:.]+$/i;

/** A syntactically valid IP or null. A missing header is never replaced by a client-controlled X-Forwarded-For. */
export function validIp(v: string | null | undefined): string | null {
  const s = v?.trim();
  if (!s || s.length > 45) return null;
  if (IPV4.test(s)) return s.split(".").every((o) => Number(o) <= 255) ? s : null;
  return s.includes(":") && IPV6.test(s) ? s : null;
}
