import { Stack, Text, Title } from "@mantine/core";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { NewsList } from "@/components/NewsList";
import { isLocale } from "@/lib/locales";
import { BLOG_CATEGORY } from "@/lib/sections";
import { SITE_URL, buildMetadata, localeUrl, safeJsonLd } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return {
    ...buildMetadata({ locale, path: "/articles", title: t("articles.title"), description: t("articles.meta_description") }),
    alternates: { ...buildMetadata({ locale, path: "/articles", title: "", description: "" }).alternates, types: { "application/rss+xml": `${SITE_URL}/${locale}/articles/feed.xml` } },
  };
}

export default async function Articles({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  const t = await getT(locale);
  const blog = { "@context": "https://schema.org", "@type": "Blog", name: t("articles.title"), description: t("articles.meta_description"), url: localeUrl(locale, "/articles"), inLanguage: locale, publisher: { "@type": "Organization", name: "Beeloy", url: SITE_URL } };
  return (
    <>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: safeJsonLd(blog) }} />
      <Stack gap={4} mb="md">
        <Title order={1}>{t("articles.title")}</Title>
        <Text c="dimmed">{t("articles.subtitle")}</Text>
      </Stack>
      <NewsList categories={[]} only={BLOG_CATEGORY} />
    </>
  );
}
