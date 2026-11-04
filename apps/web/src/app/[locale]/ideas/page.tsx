import { Title } from "@mantine/core";
import type { Metadata } from "next";
import { IdeasBrowser } from "@/components/IdeasBrowser";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "/ideas", title: t("ideas.title"), description: t("ideas.meta_description") });
}

export default async function Page({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  const t = await getT(locale);
  return (
    <>
      <Title order={1} mb="md">{t("ideas.title")}</Title>
      <IdeasBrowser />
    </>
  );
}
