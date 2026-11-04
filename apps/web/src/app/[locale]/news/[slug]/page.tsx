import type { Metadata } from "next";
import { notFound, permanentRedirect } from "next/navigation";
import { ArticleBody } from "@/components/ArticleBody";
import { CommentThread } from "@/components/CommentThread";
import { RestrictedArticle } from "@/components/RestrictedArticle";
import type { ArticleView, Category } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { isLocale } from "@/lib/locales";
import { SITE_URL, buildMetadata, localeUrl, safeJsonLd } from "@/lib/seo";
import { getT } from "@/lib/server";

async function load(slug: string, locale: string) {
  try {
    const r = await api<ArticleView>(`/api/v1/articles/${slug}`, { query: { locale }, cache: "no-store" });
    return { article: r.data, canonical: String(r.raw.canonical_slug ?? slug) };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export async function generateMetadata({ params }: { params: Promise<{ locale: string; slug: string }> }): Promise<Metadata> {
  const { locale, slug } = await params;
  const t = await getT(locale);
  const r = await load(slug, locale);
  if (!r) return buildMetadata({ locale, path: `/news/${slug}`, title: t("error.not_found"), description: "", noindex: true });
  const a = r.article;
  // a page shown in another language than requested is not an equivalent translation: point to the shown one, no indexing
  const fallback = a.fallback_from !== null;
  return buildMetadata({
    locale: fallback ? a.locale : locale, path: `/news/${r.canonical}`, title: a.title, description: a.summary,
    availableLocales: a.available_locales, noindex: fallback, ogImage: a.cover_url, type: "article",
  });
}

export default async function ArticlePage({ params }: { params: Promise<{ locale: string; slug: string }> }) {
  const { locale, slug } = await params;
  if (!isLocale(locale)) notFound();
  const r = await load(slug, locale);
  let cats: Category[] = [];
  try { cats = (await api<Category[]>("/api/v1/categories", { next: { revalidate: 300 } })).data; } catch { /* labels fall back to keys */ }
  const labels = Object.fromEntries(cats.map((c) => [c.key, c.names[locale] ?? c.names.en ?? c.key]));
  if (!r) return <RestrictedArticle slug={slug} categories={labels} />;
  if (r.canonical !== slug) permanentRedirect(`/${locale}/news/${r.canonical}`);
  const a = r.article;
  const ld = {
    "@context": "https://schema.org", "@type": "NewsArticle", headline: a.title, datePublished: a.published_at, dateModified: a.published_at,
    inLanguage: a.locale, mainEntityOfPage: localeUrl(locale, `/news/${a.slug}`), author: { "@type": "Person", name: a.author_name },
    publisher: { "@type": "Organization", name: "Beeloy", url: SITE_URL }, ...(a.cover_url ? { image: [a.cover_url] } : {}),
  };
  return (
    <>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: safeJsonLd(ld) }} />
      <ArticleBody article={a} locale={locale} categoryLabel={labels[a.category_key] ?? a.category_key} />
      <CommentThread targetType="article" targetId={a.id} />
    </>
  );
}
