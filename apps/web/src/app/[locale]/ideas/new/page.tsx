import { Title } from "@mantine/core";
import type { Metadata } from "next";
import { IdeaForm } from "@/components/IdeaForm";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "/ideas/new", title: t("ideas.submit"), description: t("ideas.submit"), noindex: true });
}

export default async function Page({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  const t = await getT(locale);
  return (
    <>
      <Title order={1} mb="md">{t("ideas.new_title")}</Title>
      <IdeaForm />
    </>
  );
}
