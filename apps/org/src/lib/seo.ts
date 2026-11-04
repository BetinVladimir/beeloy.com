import type { Metadata } from "next";
import { DEFAULT_LOCALE, LOCALES } from "./locales";

export const SITE_URL = (process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3100").replace(/\/$/, "");
export const COM_URL = (process.env.NEXT_PUBLIC_COM_URL ?? "http://localhost:3000").replace(/\/$/, "");

/** Absolute URL on beeloy.org (canonical and sitemap live here, never on .com). */
export const orgUrl = (locale: string, path = "") => `${SITE_URL}/${locale}${path}`;
/** Link to the community site; the locale travels in the URL, no cookie is shared. */
export const comUrl = (locale: string, path = "") => `${COM_URL}/${locale}${path}`;

export function buildMetadata(o: { locale: string; path: string; title: string; description: string }): Metadata {
  return {
    title: o.title,
    description: o.description,
    alternates: {
      canonical: orgUrl(o.locale, o.path),
      languages: { ...Object.fromEntries(LOCALES.map((l) => [l, orgUrl(l, o.path)])), "x-default": orgUrl(DEFAULT_LOCALE, o.path) },
    },
    openGraph: { title: o.title, description: o.description, url: orgUrl(o.locale, o.path), siteName: "Beeloy", type: "website", locale: o.locale },
  };
}
