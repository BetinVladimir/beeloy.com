import { Title } from "@mantine/core";
import type { Metadata } from "next";
import { notFound, redirect } from "next/navigation";
import { Markdown } from "@/components/Markdown";
import { loadMessages, createT } from "@/lib/i18n";
import { legalUrl } from "@/lib/legal";
import { isLocale } from "@/lib/locales";
import { buildMetadata } from "@/lib/seo";

const DOCS = ["privacy", "cookies", "terms", "rules"];

export async function generateMetadata({ params }: { params: Promise<{ locale: string; doc: string }> }): Promise<Metadata> {
  const { locale, doc } = await params;
  if (!DOCS.includes(doc)) return {};
  const t = createT(await loadMessages(locale), locale);
  return buildMetadata({ locale, path: `/legal/${doc}`, title: t(`legal.${doc}.title`), description: t(`legal.${doc}.title`) });
}

export default async function Page({ params }: { params: Promise<{ locale: string; doc: string }> }) {
  const { locale, doc } = await params;
  if (!isLocale(locale) || !DOCS.includes(doc)) notFound();
  const external = legalUrl(locale, doc);
  if (external) redirect(external); // the maintained text lives on the legal site
  const t = createT(await loadMessages(locale), locale);
  return (
    <>
      <Title order={1} mb="md">{t(`legal.${doc}.title`)}</Title>
      <Markdown lang={locale}>{t(`legal.${doc}.body`)}</Markdown>
    </>
  );
}
