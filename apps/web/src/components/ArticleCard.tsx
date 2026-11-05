import { Badge, Card, Group, Text, Title } from "@mantine/core";
import Link from "next/link";
import type { ArticleListItem } from "@/contracts/api-types";
import { formatDate } from "@/lib/i18n";
import { articlePath } from "@/lib/sections";
import classes from "./ArticleCard.module.css";

export function ArticleCard({ item, locale, categoryLabel }: { item: ArticleListItem; locale: string; categoryLabel?: string }) {
  const href = articlePath(locale, item.category_key, item.slug);
  return (
    <Card withBorder component="article" p={0} className={classes.card} style={{ overflow: "hidden" }}>
      {item.cover_url && (
        // eslint-disable-next-line @next/next/no-img-element
        <img src={item.cover_url} alt="" loading="lazy" style={{ display: "block", width: "100%", aspectRatio: "16 / 9", objectFit: "cover" }} />
      )}
      <div style={{ padding: "var(--mantine-spacing-md)" }}>
        <Group justify="space-between" mb="xs">
          {categoryLabel ? <Badge variant="light" radius="sm">{categoryLabel}</Badge> : <span />}
          <Text size="xs" c="dimmed"><time dateTime={item.published_at}>{formatDate(item.published_at, locale)}</time></Text>
        </Group>
        <Title order={3} fz="lg" mb={4} lh={1.3} className={classes.title}>
          <Link href={href} style={{ color: "inherit", textDecoration: "none" }} lang={item.locale}>{item.title}</Link>
        </Title>
        <Text size="sm" c="dimmed" lang={item.locale} lineClamp={3}>{item.summary}</Text>
      </div>
    </Card>
  );
}
