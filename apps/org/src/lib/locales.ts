import data from "@/contracts/locales.json";

export interface LocaleInfo { code: string; name: string; english_name: string; dir: "ltr" | "rtl" }
export type Locale = string;

export const LOCALE_LIST: LocaleInfo[] = data.locales as LocaleInfo[];
export const LOCALES: Locale[] = LOCALE_LIST.map((l) => l.code);
export const DEFAULT_LOCALE: Locale = data.default;

export function isLocale(s: string | undefined | null): s is Locale {
  return !!s && LOCALES.includes(s);
}

export function getDir(locale: Locale): "ltr" | "rtl" {
  return LOCALE_LIST.find((l) => l.code === locale)?.dir ?? "ltr";
}

/** Accept-Language → supported locale (primary subtag). No cookie is read: the showcase has none. */
export function detectLocale(acceptLanguage: string | null | undefined): Locale {
  const items = (acceptLanguage ?? "")
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
  return DEFAULT_LOCALE;
}
