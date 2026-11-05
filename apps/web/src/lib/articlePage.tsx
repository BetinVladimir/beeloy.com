import { SimpleGrid, Stack, Title } from "@mantine/core";
import type { Metadata } from "next";
import { notFound, permanentRedirect } from "next/navigation";
import { ArticleBody } from "@/components/ArticleBody";
import { ArticleCard } from "@/components/ArticleCard";
import { CommentThread } from "@/components/CommentThread";
import { RestrictedArticle } from "@/components/RestrictedArticle";
import type { ArticleListItem, ArticleView, Category } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { isLocale } from "@/lib/locales";
import { type Section, articlePath, sectionOf } from "@/lib/sections";
import { SITE_URL, buildMetadata, localeUrl, safeJsonLd } from "@/lib/seo";
import { getT } from "@/lib/server";

type Params = { params: Promise<{ locale: string; slug: string }> };

async function load(slug: string, locale: string) {
  try {
    const r = await api<ArticleView>(`/api/v1/articles/${slug}`, { query: { locale }, cache: "no-store" });
    return { article: r.data, canonical: String(r.raw.canonical_slug ?? slug) };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

/** One page implementation for /news/{slug} and /articles/{slug}; an article opened in the wrong section is redirected to its own. */
export function articlePage(section: Section) {
  async function generateMetadata({ params }: Params): Promise<Metadata> {
    const { locale, slug } = await params;
    const t = await getT(locale);
    const r = await load(slug, locale);
    if (!r) return buildMetadata({ locale, path: `/${section}/${slug}`, title: t("error.not_found"), description: "", noindex: true });
    const a = r.article;
    // a page shown in another language than requested is not an equivalent translation: point to the shown one, no indexing
    const fallback = a.fallback_from !== null;
    return buildMetadata({
      locale: fallback ? a.locale : locale, path: `/${sectionOf(a.category_key)}/${r.canonical}`, title: a.title, description: a.summary,
      availableLocales: a.available_locales, noindex: fallback, ogImage: a.cover_url, type: "article",
    });
  }

  async function Page({ params }: Params) {
    const { locale, slug } = await params;
    if (!isLocale(locale)) notFound();
    const t = await getT(locale);
    const r = await load(slug, locale);
    let cats: Category[] = [];
    try { cats = (await api<Category[]>("/api/v1/categories", { next: { revalidate: 300 } })).data; } catch { /* labels fall back to keys */ }
    const labels = Object.fromEntries(cats.map((c) => [c.key, c.names[locale] ?? c.names.en ?? c.key]));
    if (!r) return <RestrictedArticle slug={slug} categories={labels} />;
    const a = r.article;
    if (r.canonical !== slug || sectionOf(a.category_key) !== section) permanentRedirect(articlePath(locale, a.category_key, r.canonical));
    const blog = section === "articles";
    const ld = {
      "@context": "https://schema.org", "@type": blog ? "BlogPosting" : "NewsArticle", headline: a.title, datePublished: a.published_at, dateModified: a.published_at,
      inLanguage: a.locale, mainEntityOfPage: localeUrl(locale, `/${section}/${a.slug}`),
      author: { "@type": "Person", name: a.author_name, ...(a.author_slug ? { url: localeUrl(locale, `/people/${a.author_slug}`) } : {}) },
      publisher: { "@type": "Organization", name: "Beeloy", url: SITE_URL }, ...(a.cover_url ? { image: [a.cover_url] } : {}),
    };
    let more: ArticleListItem[] = [];
    if (blog) {
      try {
        more = (await api<ArticleListItem[]>("/api/v1/articles", { query: { locale, category: a.category_key, limit: 4 }, next: { revalidate: 60, tags: ["articles"] } })).data.filter((x) => x.id !== a.id).slice(0, 3);
      } catch { /* the post is complete without the list */ }
    }
    return (
      <>
        <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: safeJsonLd(ld) }} />
        <ArticleBody article={a} locale={locale} categoryLabel={labels[a.category_key] ?? a.category_key} />
        <CommentThread targetType="article" targetId={a.id} />
        {more.length > 0 && (
          <Stack mt="xl" component="section">
            <Title order={2} fz="xl">{t("articles.more")}</Title>
            <SimpleGrid cols={{ base: 1, sm: 3 }}>
              {more.map((m) => <ArticleCard key={m.id} item={m} locale={locale} />)}
            </SimpleGrid>
          </Stack>
        )}
      </>
    );
  }

  return { generateMetadata, Page };
}
