import data from "@/contracts/locales.json";

export interface LocaleInfo { code: string; name: string; english_name: string; dir: "ltr" | "rtl" }
export type Locale = string;

export const LOCALE_LIST: LocaleInfo[] = data.locales as LocaleInfo[];
export const LOCALES: Locale[] = LOCALE_LIST.map((l) => l.code);
export const DEFAULT_LOCALE: Locale = data.default;
export const FALLBACK_LOCALE: Locale = data.fallback;

export function isLocale(s: string | undefined | null): s is Locale {
  return !!s && LOCALES.includes(s);
}

export function getDir(locale: Locale, list: LocaleInfo[] = LOCALE_LIST): "ltr" | "rtl" {
  return list.find((l) => l.code === locale)?.dir ?? "ltr";
}

/** Maps an Accept-Language header to a supported locale (primary subtag only), or undefined. */
export function matchAcceptLanguage(header: string | null | undefined): Locale | undefined {
  if (!header) return undefined;
  const items = header
    .split(",")
    .map((part) => {
      const [tag, ...params] = part.trim().split(";");
      const q = params.map((p) => p.trim()).find((p) => p.startsWith("q="));
      return { tag: tag.toLowerCase(), q: q ? parseFloat(q.slice(2)) : 1 };
    })
    .filter((i) => i.tag && i.tag !== "*" && !Number.isNaN(i.q) && i.q > 0)
    .sort((a, b) => b.q - a.q);
  for (const { tag } of items) {
    const primary = tag.split("-")[0];
    if (isLocale(primary)) return primary;
  }
  return undefined;
}

/** cookie bl_locale (if valid) → Accept-Language → default. */
export function detectLocale(cookieValue: string | undefined | null, acceptLanguage: string | null | undefined): Locale {
  if (isLocale(cookieValue)) return cookieValue;
  return matchAcceptLanguage(acceptLanguage) ?? DEFAULT_LOCALE;
}
