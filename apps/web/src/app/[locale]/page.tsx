import { Grid, GridCol, Group, Stack, Text, Title } from "@mantine/core";
import Link from "next/link";
import type { Metadata } from "next";
import { LinkAnchor, LinkButton } from "@/components/Links";
import { notFound } from "next/navigation";
import { ArticleCard } from "@/components/ArticleCard";
import type { ArticleListItem } from "@/contracts/api-types";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/i18n";
import { articlePath } from "@/lib/sections";
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
    latest = (await api<ArticleListItem[]>("/api/v1/articles", { query: { locale, limit: 9 }, next: { revalidate: 60, tags: ["articles"] } })).data;
  } catch {
    /* the page still renders without the list */
  }
  const org = { "@context": "https://schema.org", "@type": "Organization", name: "Beeloy", url: SITE_URL };
  const [hero, ...rest] = latest;
  const live = rest.slice(0, 7);
  return (
    <>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: safeJsonLd(org) }} />
      {latest.length === 0 ? (
        <Stack gap="sm" py="xl">
          <Title order={1}>{t("home.hero_title")}</Title>
          <Text size="lg" c="dimmed" maw={680}>{t("home.hero_text")}</Text>
          <Text c="dimmed">{t("news.empty")}</Text>
        </Stack>
      ) : (
        <Grid gap="xl">
          <GridCol span={{ base: 12, md: 3 }} order={{ base: 2, md: 1 }}>
            <Group justify="space-between" mb="sm">
              <Title order={2} fz="lg">{t("home.latest_news")}</Title>
            </Group>
            <Stack gap={0}>
              {live.map((a) => (
                <Group key={a.id} justify="space-between" align="flex-start" wrap="nowrap" py="sm" style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
                  <Link href={articlePath(locale, a.category_key, a.slug)} lang={a.locale} style={{ color: "inherit", textDecoration: "none", fontWeight: 600, lineHeight: 1.35 }}>{a.title}</Link>
                  <Text size="xs" c="dimmed" style={{ whiteSpace: "nowrap" }}><time dateTime={a.published_at}>{formatDate(a.published_at, locale)}</time></Text>
                </Group>
              ))}
            </Stack>
            <LinkAnchor href={`/${locale}/news`} mt="md" display="inline-block">{t("home.all_news")}</LinkAnchor>
          </GridCol>
          <GridCol span={{ base: 12, md: 6 }} order={{ base: 1, md: 2 }}>
            <ArticleCard item={hero} locale={locale} />
          </GridCol>
          <GridCol span={{ base: 12, md: 3 }} order={3}>
            <Stack gap="sm">
              <Title order={1} fz="xl" lh={1.25}>{t("home.hero_title")}</Title>
              <Text size="sm" c="dimmed">{t("home.hero_text")}</Text>
              <LinkButton href={`/${locale}/ideas`} fullWidth>{t("home.share_idea")}</LinkButton>
              <LinkButton href={`/${locale}/news`} variant="default" fullWidth>{t("home.read_news")}</LinkButton>
            </Stack>
          </GridCol>
        </Grid>
      )}
    </>
  );
}
