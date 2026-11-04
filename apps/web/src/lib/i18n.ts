import fallbackEn from "@/contracts/fallback-en.json";
import { FALLBACK_LOCALE, type Locale } from "./locales";

export type Messages = Record<string, string>;
export type TFunction = (key: string, params?: Record<string, string | number>) => string;

/** API base for server-side fetches (Worker → api-site). */
export function apiOrigin(): string {
  return process.env.API_ORIGIN ?? "http://localhost:8080";
}

/** Loads the dictionary of a locale; missing keys (or an unavailable API) fall back to the built-in English one. */
export async function loadMessages(locale: Locale, fetchImpl: typeof fetch = fetch): Promise<Messages> {
  const base = fallbackEn as Messages;
  try {
    const res = await fetchImpl(`${apiOrigin()}/api/v1/i18n/${locale}`, { next: { revalidate: 300, tags: ["i18n"] } } as RequestInit);
    if (!res.ok) return base;
    const data = (await res.json()) as Messages;
    return { ...base, ...data };
  } catch {
    return base;
  }
}

/** Builds t(): key lookup, plural forms via Intl.PluralRules (`key.one`, `key.other`…), {param} substitution. */
export function createT(messages: Messages, locale: Locale = FALLBACK_LOCALE): TFunction {
  const rules = new Intl.PluralRules(locale);
  return (key, params) => {
    let text: string | undefined;
    if (params && typeof params.count === "number") {
      text = messages[`${key}.${rules.select(params.count)}`] ?? messages[`${key}.other`];
    }
    text ??= messages[key];
    if (text === undefined) {
      if (process.env.NODE_ENV !== "production") console.warn(`[i18n] missing key: ${key}`);
      return key;
    }
    if (!params) return text;
    return text.replace(/\{([a-zA-Z_][a-zA-Z0-9_]*)\}/g, (m, name: string) => (name in params ? String(params[name]) : m));
  };
}

export function formatDate(d: string | Date, locale: Locale, opts: Intl.DateTimeFormatOptions = { dateStyle: "medium" }): string {
  try {
    return new Intl.DateTimeFormat(locale, opts).format(typeof d === "string" ? new Date(d) : d);
  } catch {
    return String(d);
  }
}
