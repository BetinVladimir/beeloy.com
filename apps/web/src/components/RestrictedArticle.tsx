"use client";
import { Stack, Text } from "@mantine/core";
import { useEffect, useState } from "react";
import type { ArticleView } from "@/contracts/api-types";
import { api } from "@/lib/api";
import { ArticleBody } from "./ArticleBody";
import { CommentThread } from "./CommentThread";
import { useLocale, useSession, useT } from "./Providers";
import { Spinner } from "./States";

/** Articles with restricted visibility are not cached publicly: the signed-in visitor's browser asks for them. */
export function RestrictedArticle({ slug, categories }: { slug: string; categories: Record<string, string> }) {
  const t = useT();
  const locale = useLocale();
  const { me, loading } = useSession();
  const [a, setA] = useState<ArticleView | null | undefined>(undefined);
  useEffect(() => {
    if (loading) return;
    if (!me) { setA(null); return; }
    api<ArticleView>(`/api/v1/articles/${slug}`, { query: { locale } }).then((r) => setA(r.data)).catch(() => setA(null));
  }, [loading, me, slug, locale]);
  if (a === undefined) return <Spinner />;
  if (a === null) return <Text>{t("error.not_found")}</Text>;
  return (
    <Stack>
      <ArticleBody article={a} locale={locale} categoryLabel={categories[a.category_key] ?? a.category_key} />
      <CommentThread targetType="article" targetId={a.id} />
    </Stack>
  );
}
