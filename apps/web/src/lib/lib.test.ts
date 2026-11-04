import { describe, expect, it, vi } from "vitest";
import { ApiError, api, buildUrl } from "./api";
import { backoffDelay, mergeMessages } from "./chat";
import { createT, formatDate, loadMessages } from "./i18n";
import { DEFAULT_LOCALE, LOCALES, LOCALE_LIST, detectLocale, getDir, isLocale, matchAcceptLanguage } from "./locales";
import { buildTarget, validIp } from "./proxyTarget";
import { sanitizeReturnTo } from "./returnTo";
import { buildMetadata, safeJsonLd } from "./seo";
import fallback from "@/contracts/fallback-en.json";
import type { Message } from "@/contracts/api-types";

describe("locales", () => {
  it("has the 26 supported languages (24 EU + ru + uk)", () => {
    expect(LOCALES).toHaveLength(26);
    expect(new Set(LOCALES).size).toBe(26);
    expect(DEFAULT_LOCALE).toBe("bg");
    for (const l of LOCALE_LIST) {
      expect(l.english_name).toBeTruthy();
      expect(["ltr", "rtl"]).toContain(l.dir);
    }
    for (const c of ["bg", "en", "ru", "uk", "ga", "mt"]) expect(isLocale(c)).toBe(true);
    for (const c of ["tr", "zh", "ar", "xx", "", undefined]) expect(isLocale(c as string)).toBe(false);
  });
  it("getDir supports rtl languages from a list", () => {
    expect(getDir("bg")).toBe("ltr");
    expect(getDir("ar", [{ code: "ar", name: "x", english_name: "Arabic", dir: "rtl" }])).toBe("rtl");
    expect(getDir("unknown")).toBe("ltr");
  });
  it.each([
    ["ru-RU,ru;q=0.9,en;q=0.8", "ru"],
    ["pt-BR", "pt"],
    ["xx, en;q=0.5", "en"],
    ["fr;q=0.1, de;q=0.9", "de"],
    ["*", undefined],
    ["tr", undefined],
    ["", undefined],
  ])("Accept-Language %s -> %s", (header, want) => {
    expect(matchAcceptLanguage(header)).toBe(want);
  });
  it("cookie beats Accept-Language, the default is the last resort", () => {
    expect(detectLocale("de", "ru")).toBe("de");
    expect(detectLocale("zz", "ru")).toBe("ru");
    expect(detectLocale(undefined, "tr")).toBe("bg");
    expect(detectLocale(null, null)).toBe("bg");
  });
});

describe("createT", () => {
  const m = { hello: "Hello {name}", "items.one": "{count} item", "items.other": "{count} items", plain: "Plain" };
  it("substitutes params and keeps unknown placeholders", () => {
    const t = createT(m, "en");
    expect(t("hello", { name: "Ann" })).toBe("Hello Ann");
    expect(t("hello")).toBe("Hello {name}");
    expect(t("hello", { other: 1 })).toBe("Hello {name}");
  });
  it("selects plural forms", () => {
    const t = createT(m, "en");
    expect(t("items", { count: 1 })).toBe("1 item");
    expect(t("items", { count: 5 })).toBe("5 items");
  });
  it("falls back to .other for languages with more forms", () => {
    expect(createT(m, "ru")("items", { count: 2 })).toBe("2 items");
  });
  it("returns the key for missing entries", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(createT(m, "en")("nope.key")).toBe("nope.key");
    warn.mockRestore();
  });
  it("formats dates and tolerates bad input", () => {
    expect(formatDate("2026-11-04T10:00:00Z", "en")).toContain("2026");
    expect(typeof formatDate("garbage", "en")).toBe("string");
  });
});

describe("loadMessages", () => {
  it("merges the API dictionary over the built-in English one", async () => {
    const f = vi.fn(async () => new Response(JSON.stringify({ "app.name": "Бийлой" }), { status: 200 }));
    const msgs = await loadMessages("bg", f as unknown as typeof fetch);
    expect(msgs["app.name"]).toBe("Бийлой");
    expect(msgs["nav.news"]).toBe((fallback as Record<string, string>)["nav.news"]);
  });
  it("uses the built-in dictionary when the API fails", async () => {
    const f = vi.fn(async () => { throw new Error("down"); });
    expect(await loadMessages("bg", f as unknown as typeof fetch)).toEqual(fallback);
    const bad = vi.fn(async () => new Response("x", { status: 503 }));
    expect(await loadMessages("bg", bad as unknown as typeof fetch)).toEqual(fallback);
  });
});

describe("sanitizeReturnTo", () => {
  it.each([
    ["/ok", "/ok"], ["/ok?x=1#a", "/ok?x=1#a"], ["//evil.com", "/"], ["/\\evil", "/"], ["https://x.com", "/"], ["javascript:alert(1)", "/"],
    ["", "/"], [null, "/"], [undefined, "/"], ["/a\nb", "/"], ["/" + "a".repeat(250), "/"], ["relative", "/"],
  ])("%s -> %s", (input, want) => {
    expect(sanitizeReturnTo(input as string, "/")).toBe(want);
  });
});

describe("api client", () => {
  it("builds URLs without empty params", () => {
    expect(buildUrl("", "/api/v1/x", { a: 1, b: "", c: undefined, d: "z" })).toBe("/api/v1/x?a=1&d=z");
    expect(buildUrl("http://api", "/api/v1/x", { a: 1 })).toBe("http://api/api/v1/x?a=1");
  });
  it("sends the CSRF header on unsafe methods and unwraps data", async () => {
    const f = vi.fn(async () => new Response(JSON.stringify({ data: { ok: true }, next_cursor: "c" }), { status: 200 }));
    vi.stubGlobal("fetch", f);
    const r = await api<{ ok: boolean }>("/api/v1/x", { method: "POST", body: { a: 1 } });
    expect(r.data.ok).toBe(true);
    expect(r.next_cursor).toBe("c");
    const init = (f.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>)["X-Requested-With"]).toBe("beeloy");
    await api("/api/v1/x");
    expect(((f.mock.calls[1] as unknown as [string, RequestInit])[1].headers as Record<string, string>)["X-Requested-With"]).toBeUndefined();
  });
  it("throws ApiError with code and fields", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "validation_failed", message: "m", fields: { title: "too_short" } } }), { status: 422 })));
    await expect(api("/api/v1/x")).rejects.toMatchObject({ status: 422, code: "validation_failed", fields: { title: "too_short" } });
    vi.stubGlobal("fetch", vi.fn(async () => new Response("not json", { status: 502 })));
    const e = await api("/api/v1/x").catch((x) => x);
    expect(e).toBeInstanceOf(ApiError);
    expect(e.status).toBe(502);
  });
  it("handles 204", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 204 })));
    expect((await api("/api/v1/x", { method: "DELETE" })).data).toBeUndefined();
  });
});

describe("proxy target", () => {
  it("forwards only /api/v1/** and rejects traversal", () => {
    expect(buildTarget("https://api-site.beeloy.com/", ["v1", "ideas"], "?a=1")).toBe("https://api-site.beeloy.com/api/v1/ideas?a=1");
    expect(buildTarget("https://x", ["metrics"], "")).toBeNull();
    expect(buildTarget("https://x", ["v1", "..", "admin"], "")).toBeNull();
    expect(buildTarget("https://x", [], "")).toBeNull();
    expect(buildTarget("https://x", ["v2", "x"], "")).toBeNull();
  });
});

describe("seo", () => {
  it("builds canonical and hreflang only for available locales", () => {
    const m = buildMetadata({ locale: "de", path: "/news/a", title: "T", description: "D", availableLocales: ["en", "de"] });
    expect(m.alternates?.canonical).toMatch(/\/de\/news\/a$/);
    const langs = m.alternates?.languages as Record<string, string>;
    expect(Object.keys(langs).sort()).toEqual(["de", "en", "x-default"]);
    expect(langs["x-default"]).toMatch(/\/en\/news\/a$/);
  });
  it("covers all locales by default and supports noindex", () => {
    const m = buildMetadata({ locale: "bg", path: "", title: "T", description: "D", noindex: true });
    expect(Object.keys(m.alternates?.languages as object)).toHaveLength(27);
    expect(m.robots).toEqual({ index: false, follow: false });
  });
  it("escapes < in JSON-LD", () => {
    expect(safeJsonLd({ a: "</script><b>" })).not.toContain("</script>");
  });
});

describe("chat helpers", () => {
  const msg = (o: Partial<Message> & { pending?: boolean }): Message & { pending?: boolean } => ({ id: "i", conversation_id: "c", seq: 1, sender_id: "u", client_id: "k", body: "b", deleted: false, created_at: "2026-01-01T00:00:00Z", ...o });
  it("deduplicates by id and lets the server copy replace a pending one", () => {
    const pending = msg({ id: "pending-k", seq: 0, client_id: "k", pending: true });
    const merged = mergeMessages([pending], [msg({ id: "real", seq: 4, client_id: "k" })]);
    expect(merged).toHaveLength(1);
    expect(merged[0].id).toBe("real");
    expect(mergeMessages(merged, [msg({ id: "real", seq: 4, client_id: "k" })])).toHaveLength(1);
  });
  it("orders by seq with pending messages last", () => {
    const m = mergeMessages([msg({ id: "p", seq: 0, client_id: "p", pending: true })], [msg({ id: "b", seq: 2, client_id: "b" }), msg({ id: "a", seq: 1, client_id: "a" })]);
    expect(m.map((x) => x.id)).toEqual(["a", "b", "p"]);
  });
  it("backs off 1,2,4,8,16,30 seconds with jitter", () => {
    const base = [1000, 2000, 4000, 8000, 16000, 30000, 30000];
    base.forEach((b, i) => {
      expect(backoffDelay(i, () => 0.5)).toBe(b);
      expect(backoffDelay(i, () => 0)).toBe(Math.round(b * 0.8));
      expect(backoffDelay(i, () => 1)).toBe(Math.round(b * 1.2));
    });
  });
});

describe("validIp", () => {
  it("accepts well-formed addresses only; a missing header is never replaced", () => {
    expect(validIp("203.0.113.7")).toBe("203.0.113.7");
    expect(validIp(" 2001:db8::1 ")).toBe("2001:db8::1");
    expect(validIp("999.1.1.1")).toBeNull();
    expect(validIp("1.2.3.4, 5.6.7.8")).toBeNull();
    expect(validIp("<script>")).toBeNull();
    expect(validIp(null)).toBeNull();
    expect(validIp("")).toBeNull();
  });
});

describe("legalUrl", () => {
  it("sends Terms, Privacy and Cookies to the legal site; unsupported languages get English; rules stay in the app", async () => {
    const { legalUrl } = await import("./legal");
    expect(legalUrl("bg", "terms")).toBe("https://legal.beeloy.org/bg/terms");
    expect(legalUrl("uk", "cookies")).toBe("https://legal.beeloy.org/en/cookies");
    expect(legalUrl("en", "rules")).toBeNull();
    expect(legalUrl("en", "x")).toBeNull();
  });
});
