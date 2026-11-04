import type { ApiErrorBody } from "@/contracts/api-types";
import { validIp } from "./proxyTarget";

export class ApiError extends Error {
  code: string;
  status: number;
  fields?: Record<string, string>;
  constructor(status: number, code: string, message: string, fields?: Record<string, string>) {
    super(message);
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export interface ApiResult<T> { data: T; next_cursor?: string | null; raw: Record<string, unknown> }

export interface Opts {
  method?: string;
  body?: unknown;
  query?: Record<string, string | number | undefined | null>;
  signal?: AbortSignal;
  /** server side only */
  next?: { revalidate?: number; tags?: string[] };
  cache?: RequestCache;
  /** server side: forward the visitor's cookies (private data) */
  cookie?: string;
}

const UNSAFE = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export function buildUrl(base: string, path: string, query?: Opts["query"]): string {
  const url = new URL(path, base || "http://localhost");
  if (query) for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null && v !== "") url.searchParams.set(k, String(v));
  return base ? url.toString() : url.pathname + url.search;
}

export function errorFrom(status: number, body: unknown): ApiError {
  const e = (body as ApiErrorBody | undefined)?.error;
  return new ApiError(status, e?.code ?? "internal", e?.message ?? "error", e?.fields);
}

/** Server-side calls travel the same trusted path as the /api proxy: Worker secret + the real client IP (CF-Connecting-IP only). */
async function trustedHeaders(): Promise<Record<string, string>> {
  const out: Record<string, string> = {};
  const secret = process.env.PROXY_SHARED_SECRET;
  if (secret) out["X-Proxy-Secret"] = secret;
  try {
    const { headers } = await import("next/headers");
    const ip = validIp((await headers()).get("cf-connecting-ip"));
    if (ip) out["X-Real-Client-IP"] = ip;
  } catch {
    /* no request context (build time): no client IP */
  }
  return out;
}

/** Works in the browser (same origin, cookies) and on the server (needs API_ORIGIN). */
export async function api<T = unknown>(path: string, opts: Opts = {}): Promise<ApiResult<T>> {
  const method = (opts.method ?? "GET").toUpperCase();
  const isServer = typeof window === "undefined";
  const base = isServer ? (process.env.API_ORIGIN ?? "http://localhost:8080") : "";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (UNSAFE.has(method)) headers["X-Requested-With"] = "beeloy";
  if (isServer && opts.cookie) headers["Cookie"] = opts.cookie;
  if (isServer) Object.assign(headers, await trustedHeaders());
  const init: RequestInit & { next?: Opts["next"] } = { method, headers, signal: opts.signal, cache: opts.cache };
  if (!isServer) init.credentials = "same-origin";
  if (opts.body !== undefined) init.body = JSON.stringify(opts.body);
  if (isServer && opts.next) init.next = opts.next;
  const res = await fetch(buildUrl(base, path, opts.query), init);
  if (res.status === 204) return { data: undefined as T, raw: {} };
  let json: Record<string, unknown> = {};
  try {
    json = (await res.json()) as Record<string, unknown>;
  } catch {
    /* empty or non-JSON body */
  }
  if (!res.ok) throw errorFrom(res.status, json);
  return { data: json.data as T, next_cursor: (json.next_cursor as string | null | undefined) ?? null, raw: json };
}
