import type { Metadata } from "next";
import { DEFAULT_LOCALE, LOCALES, type Locale } from "./locales";

export const SITE_URL = (process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3000").replace(/\/$/, "");

export interface MetaArgs {
  locale: Locale;
  /** path without the locale prefix: "" for the home page or "/news/slug" */
  path: string;
  title: string;
  description: string;
  /** locales that really have this page; hreflang is built only for them */
  availableLocales?: Locale[];
  noindex?: boolean;
  ogImage?: string | null;
  type?: "website" | "article";
}

export function localeUrl(locale: Locale, path: string): string {
  return `${SITE_URL}/${locale}${path}`;
}

export function buildMetadata(a: MetaArgs): Metadata {
  const available = a.availableLocales ?? LOCALES;
  const languages: Record<string, string> = {};
  for (const l of available) languages[l] = localeUrl(l, a.path);
  const xDefault = available.includes(DEFAULT_LOCALE) ? DEFAULT_LOCALE : available[0];
  if (xDefault) languages["x-default"] = localeUrl(xDefault, a.path);
  const url = localeUrl(a.locale, a.path);
  return {
    title: a.title,
    description: a.description,
    alternates: { canonical: url, languages },
    robots: a.noindex ? { index: false, follow: false } : undefined,
    openGraph: {
      type: a.type ?? "website",
      locale: a.locale,
      title: a.title,
      description: a.description,
      url,
      images: a.ogImage ? [{ url: a.ogImage }] : undefined,
    },
    twitter: { card: a.ogImage ? "summary_large_image" : "summary", title: a.title, description: a.description },
  };
}

/** JSON-LD must not be able to close the script tag. */
export function safeJsonLd(data: unknown): string {
  return JSON.stringify(data).replace(/</g, "\\u003c");
}
