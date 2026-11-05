import { api } from "@/lib/api";
import { isLocale } from "@/lib/locales";
import type { ArticleListItem } from "@/contracts/api-types";
import { BLOG_CATEGORY } from "@/lib/sections";
import { SITE_URL } from "@/lib/seo";
import { getT } from "@/lib/server";

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

export async function GET(_req: Request, { params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) return new Response("not found", { status: 404 });
  const t = await getT(locale);
  let items: ArticleListItem[] = [];
  try {
    items = (await api<ArticleListItem[]>("/api/v1/articles", { query: { locale, category: BLOG_CATEGORY, limit: 30 }, cache: "no-store" })).data;
  } catch { /* an empty feed is valid */ }
  const entries = items.map((a) => {
    const url = `${SITE_URL}/${locale}/articles/${a.slug}`;
    return `<item><title>${esc(a.title)}</title><link>${url}</link><guid isPermaLink="true">${url}</guid><pubDate>${new Date(a.published_at).toUTCString()}</pubDate><description>${esc(a.summary)}</description></item>`;
  }).join("");
  const xml = `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>${esc(t("articles.title"))} · Beeloy</title><link>${SITE_URL}/${locale}/articles</link><description>${esc(t("articles.meta_description"))}</description><language>${locale}</language>${entries}</channel></rss>`;
  return new Response(xml, { headers: { "Content-Type": "application/rss+xml; charset=utf-8", "Cache-Control": "public, max-age=300" } });
}
