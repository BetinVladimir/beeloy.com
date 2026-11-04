"use client";
import { Chip, Group, SimpleGrid, Stack, Text } from "@mantine/core";
import { useState } from "react";
import type { ArticleListItem, Category } from "@/contracts/api-types";
import { ArticleCard } from "./ArticleCard";
import { useLocale, useT } from "./Providers";
import { ErrorAlert, ListSkeleton, LoadMore } from "./States";
import { usePaged } from "./usePaged";

export function NewsList({ categories }: { categories: Category[] }) {
  const t = useT();
  const locale = useLocale();
  const [cat, setCat] = useState<string>("");
  const { items, next, loading, error, more, reload } = usePaged<ArticleListItem>("/api/v1/articles", { locale, category: cat || undefined, limit: 12 }, `${locale}:${cat}`);
  const label = (key: string) => categories.find((c) => c.key === key)?.names[locale] ?? categories.find((c) => c.key === key)?.names.en ?? key;
  return (
    <Stack>
      <Chip.Group multiple={false} value={cat} onChange={(v) => setCat(v as string)}>
        <Group gap="xs">
          <Chip value="">{t("news.all_categories")}</Chip>
          {categories.map((c) => <Chip key={c.key} value={c.key}>{label(c.key)}</Chip>)}
        </Group>
      </Chip.Group>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("news.empty")}</Text>}
      <SimpleGrid cols={{ base: 1, sm: 2 }}>
        {items.map((a) => <ArticleCard key={a.id} item={a} locale={locale} categoryLabel={label(a.category_key)} />)}
      </SimpleGrid>
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}
