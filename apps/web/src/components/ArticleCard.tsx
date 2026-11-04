import { Badge, Card, Group, Text, Title } from "@mantine/core";
import Link from "next/link";
import type { ArticleListItem } from "@/contracts/api-types";
import { formatDate } from "@/lib/i18n";

export function ArticleCard({ item, locale, categoryLabel }: { item: ArticleListItem; locale: string; categoryLabel?: string }) {
  return (
    <Card withBorder component="article">
      <Group justify="space-between" mb="xs">
        {categoryLabel ? <Badge variant="light">{categoryLabel}</Badge> : <span />}
        <Text size="xs" c="dimmed"><time dateTime={item.published_at}>{formatDate(item.published_at, locale)}</time></Text>
      </Group>
      <Title order={3} fz="lg" mb={4}>
        <Link href={`/${locale}/news/${item.slug}`} style={{ color: "inherit", textDecoration: "none" }} lang={item.locale}>{item.title}</Link>
      </Title>
      <Text size="sm" c="dimmed" lang={item.locale}>{item.summary}</Text>
    </Card>
  );
}
