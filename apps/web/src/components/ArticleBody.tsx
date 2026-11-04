"use client";
import { Alert, Anchor, Badge, Group, Stack, Text, Title } from "@mantine/core";
import Link from "next/link";
import type { ArticleView } from "@/contracts/api-types";
import { formatDate } from "@/lib/i18n";
import { LOCALE_LIST } from "@/lib/locales";
import { Markdown } from "./Markdown";
import { useT } from "./Providers";

export function ArticleBody({ article, locale, categoryLabel }: { article: ArticleView; locale: string; categoryLabel: string }) {
  const t = useT();
  const name = (c: string) => LOCALE_LIST.find((l) => l.code === c)?.name ?? c;
  return (
    <article>
      <Stack gap="sm">
        <Group gap="xs">
          <Badge variant="light">{categoryLabel}</Badge>
          {article.visibility === "restricted" && <Badge color="gray">{t("article.restricted_notice")}</Badge>}
        </Group>
        <Title order={1} lang={article.locale}>{article.title}</Title>
        <Text c="dimmed" size="sm">
          <time dateTime={article.published_at}>{formatDate(article.published_at, locale)}</time>
          {article.author_slug ? <> · <Anchor component={Link} href={`/${locale}/people/${article.author_slug}`}>{article.author_name}</Anchor></> : <> · {article.author_name}</>}
        </Text>
        {article.fallback_from && <Alert color="blue" role="note">{t("article.fallback_notice", { language: name(article.locale) })}</Alert>}
        {article.origin === "machine" && <Alert color="brand" role="note">{t("article.machine_translated_notice")}</Alert>}
        <Markdown lang={article.locale}>{article.body_md}</Markdown>
        {article.available_locales.length > 1 && (
          <Group gap="xs" aria-label={t("article.languages")}>
            <Text size="sm" c="dimmed">{t("article.languages")}:</Text>
            {article.available_locales.map((l) => <Anchor key={l} component={Link} href={`/${l}/news/${article.slug}`} size="sm" lang={l}>{name(l)}</Anchor>)}
          </Group>
        )}
      </Stack>
    </article>
  );
}
