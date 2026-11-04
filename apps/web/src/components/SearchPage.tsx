"use client";
import { Avatar, Group, Stack, Tabs, Text, TextInput } from "@mantine/core";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useState } from "react";
import type { ArticleListItem, PersonHit } from "@/contracts/api-types";
import { ArticleCard } from "./ArticleCard";
import { useLocale, useT } from "./Providers";
import { ErrorAlert, ListSkeleton, LoadMore } from "./States";
import { usePaged } from "./usePaged";

function Articles({ q, locale }: { q: string; locale: string }) {
  const t = useT();
  const { items, next, loading, error, more, reload } = usePaged<ArticleListItem>("/api/v1/search/articles", { q, locale, limit: 10 }, `${q}:${locale}`);
  return (
    <Stack>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("search.empty")}</Text>}
      {items.map((a) => <ArticleCard key={a.id} item={a} locale={locale} />)}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}

function People({ q, locale }: { q: string; locale: string }) {
  const t = useT();
  const { items, next, loading, error, more, reload } = usePaged<PersonHit>("/api/v1/search/people", { q, limit: 10 }, q);
  return (
    <Stack>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("search.empty")}</Text>}
      {items.map((p) => (
        <Link key={p.id} href={`/${locale}/people/${p.slug}`} style={{ textDecoration: "none", color: "inherit" }}>
          <Group>
            <Avatar src={p.avatar_url ?? undefined} alt="" radius="xl" />
            <div><Text fw={600}>{p.nickname}</Text><Text size="sm" c="dimmed">@{p.slug}</Text></div>
          </Group>
        </Link>
      ))}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}

export function SearchPage() {
  const t = useT();
  const locale = useLocale();
  const router = useRouter();
  const sp = useSearchParams();
  const q = (sp.get("q") ?? "").trim();
  const tab = sp.get("tab") === "people" ? "people" : "articles";
  const [text, setText] = useState(q);
  const go = (nq: string, ntab: string) => router.push(`/${locale}/search?q=${encodeURIComponent(nq)}&tab=${ntab}`);
  return (
    <Stack>
      <form onSubmit={(e) => { e.preventDefault(); if (text.trim().length >= 2) go(text.trim(), tab); }} role="search">
        <TextInput value={text} onChange={(e) => setText(e.currentTarget.value)} label={t("search.title")} placeholder={t("search.placeholder")} />
      </form>
      {q.length < 2 ? <Text c="dimmed">{t("search.short_query")}</Text> : (
        <Tabs value={tab} onChange={(v) => go(q, v ?? "articles")}>
          <Tabs.List>
            <Tabs.Tab value="articles">{t("search.tab_articles")}</Tabs.Tab>
            <Tabs.Tab value="people">{t("search.tab_people")}</Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="articles" pt="md">{tab === "articles" && <Articles q={q} locale={locale} />}</Tabs.Panel>
          <Tabs.Panel value="people" pt="md">{tab === "people" && <People q={q} locale={locale} />}</Tabs.Panel>
        </Tabs>
      )}
    </Stack>
  );
}
