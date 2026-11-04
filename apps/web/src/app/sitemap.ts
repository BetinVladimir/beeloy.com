import type { MetadataRoute } from "next";
import { LOCALES } from "@/lib/locales";
import { SITE_URL } from "@/lib/seo";
import { api } from "@/lib/api";

interface Entry { path_slug: string; updated_at: string; locales: string[] }

export const dynamic = "force-dynamic"; // entries reflect the current publication state

async function all(type: "articles" | "ideas" | "users"): Promise<Entry[]> {
  const out: Entry[] = [];
  let cursor: string | undefined;
  try {
    for (let i = 0; i < 50; i++) {
      const r = await api<Entry[]>("/api/v1/seo/sitemap-entries", { query: { type, limit: 500, cursor }, cache: "no-store" });
      out.push(...r.data);
      if (!r.next_cursor) break;
      cursor = r.next_cursor;
    }
  } catch {
    /* an unavailable API gives a smaller sitemap, not an error */
  }
  return out;
}

const alt = (path: string, locales: string[]) => Object.fromEntries(locales.map((l) => [l, `${SITE_URL}/${l}${path}`]));

export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
  const entries: MetadataRoute.Sitemap = [];
  for (const p of ["", "/news", "/ideas", "/legal/privacy", "/legal/cookies", "/legal/terms", "/legal/rules"]) {
    for (const l of LOCALES) entries.push({ url: `${SITE_URL}/${l}${p}`, alternates: { languages: alt(p, LOCALES) } });
  }
  for (const a of await all("articles")) {
    const p = `/news/${a.path_slug}`;
    for (const l of a.locales) entries.push({ url: `${SITE_URL}/${l}${p}`, lastModified: a.updated_at, alternates: { languages: alt(p, a.locales) } });
  }
  for (const [type, base] of [["ideas", "ideas"], ["users", "people"]] as const) {
    for (const e of await all(type)) for (const l of LOCALES) entries.push({ url: `${SITE_URL}/${l}/${base}/${e.path_slug}`, lastModified: e.updated_at });
  }
  return entries;
}
