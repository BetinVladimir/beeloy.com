"use client";
import { Button, Group, SegmentedControl, Select, Stack, Text } from "@mantine/core";
import Link from "next/link";
import { useState } from "react";
import type { Idea } from "@/contracts/api-types";
import { IDEA_CATEGORIES, IDEA_STATUSES } from "@/contracts/idea-transitions";
import { IdeaCard } from "./IdeaParts";
import { useLocale, useSession, useT } from "./Providers";
import { ErrorAlert, ListSkeleton, LoadMore } from "./States";
import { usePaged } from "./usePaged";

export function IdeasBrowser() {
  const t = useT();
  const locale = useLocale();
  const { me } = useSession();
  const [sort, setSort] = useState("new");
  const [status, setStatus] = useState<string | null>(null);
  const [category, setCategory] = useState<string | null>(null);
  const [mine, setMine] = useState(false);
  const { items, next, loading, error, more, reload, setItems } = usePaged<Idea>(
    "/api/v1/ideas", { sort, status: status ?? undefined, category: category ?? undefined, mine: mine ? 1 : undefined, limit: 12 }, `${sort}|${status}|${category}|${mine}|${me?.id ?? ""}`);
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <Group align="flex-end">
          <SegmentedControl value={sort} onChange={setSort} data={[{ value: "new", label: t("ideas.sort.new") }, { value: "top", label: t("ideas.sort.top") }]} aria-label={t("ideas.sort.label")} />
          <Select clearable placeholder={t("ideas.filter.status")} aria-label={t("ideas.filter.status")} data={IDEA_STATUSES.map((s) => ({ value: s, label: t(`ideas.status.${s}`) }))} value={status} onChange={setStatus} w={180} />
          <Select clearable placeholder={t("ideas.filter.category")} aria-label={t("ideas.filter.category")} data={IDEA_CATEGORIES.map((c) => ({ value: c, label: t(`ideas.category.${c}`) }))} value={category} onChange={setCategory} w={180} />
          {me && <Button variant={mine ? "filled" : "default"} size="sm" onClick={() => setMine((v) => !v)} aria-pressed={mine}>{t("ideas.mine")}</Button>}
        </Group>
        <Button component={Link} href={`/${locale}/ideas/new`}>{t("ideas.submit")}</Button>
      </Group>
      {error && <ErrorAlert error={error} onRetry={reload} />}
      {loading && items.length === 0 && <ListSkeleton />}
      {!loading && !error && items.length === 0 && <Text c="dimmed">{t("ideas.empty")}</Text>}
      {items.map((i) => <IdeaCard key={i.id} idea={i} onChange={(n) => setItems((l) => l.map((x) => (x.id === n.id ? n : x)))} />)}
      <LoadMore next={next} loading={loading} onMore={() => void more()} />
    </Stack>
  );
}
