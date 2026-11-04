import { Group, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import type { Metadata } from "next";
import { LinkAnchor, LinkButton } from "@/components/Links";
import { notFound } from "next/navigation";
import { ArticleCard } from "@/components/ArticleCard";
import type { ArticleListItem } from "@/contracts/api-types";
import { api } from "@/lib/api";
import { getT } from "@/lib/server";
import { isLocale } from "@/lib/locales";
import { SITE_URL, buildMetadata, safeJsonLd } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "", title: t("home.meta_title"), description: t("home.meta_description") });
}

export default async function Home({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  const t = await getT(locale);
  let latest: ArticleListItem[] = [];
  try {
    latest = (await api<ArticleListItem[]>("/api/v1/articles", { query: { locale, limit: 6 }, next: { revalidate: 60, tags: ["articles"] } })).data;
  } catch {
    /* the page still renders without the list */
  }
  const org = { "@context": "https://schema.org", "@type": "Organization", name: "Beeloy", url: SITE_URL };
  return (
    <Stack gap="xl">
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: safeJsonLd(org) }} />
      <Stack gap="sm" py="xl">
        <Title order={1}>{t("home.hero_title")}</Title>
        <Text size="lg" c="dimmed" maw={680}>{t("home.hero_text")}</Text>
        <Group>
          <LinkButton href={`/${locale}/news`}>{t("home.read_news")}</LinkButton>
          <LinkButton href={`/${locale}/ideas`} variant="default">{t("home.share_idea")}</LinkButton>
        </Group>
      </Stack>
      <Stack>
        <Group justify="space-between">
          <Title order={2}>{t("home.latest_news")}</Title>
          <LinkAnchor href={`/${locale}/news`}>{t("home.all_news")}</LinkAnchor>
        </Group>
        {latest.length === 0 ? <Text c="dimmed">{t("news.empty")}</Text> : (
          <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }}>
            {latest.map((a) => <ArticleCard key={a.id} item={a} locale={locale} />)}
          </SimpleGrid>
        )}
      </Stack>
    </Stack>
  );
}
