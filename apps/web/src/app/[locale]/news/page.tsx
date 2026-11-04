import { Title } from "@mantine/core";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { NewsList } from "@/components/NewsList";
import type { Category } from "@/contracts/api-types";
import { api } from "@/lib/api";
import { isLocale } from "@/lib/locales";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "/news", title: t("news.title"), description: t("news.meta_description") });
}

export default async function News({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  const t = await getT(locale);
  let categories: Category[] = [];
  try {
    categories = (await api<Category[]>("/api/v1/categories", { next: { revalidate: 300 } })).data;
  } catch {
    /* filters are optional */
  }
  return (
    <>
      <Title order={1} mb="md">{t("news.title")}</Title>
      <NewsList categories={categories} />
    </>
  );
}
