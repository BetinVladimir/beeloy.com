import type { Metadata } from "next";
import { createT, loadMessages } from "@/lib/i18n";
import { buildMetadata, comUrl } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return buildMetadata({ locale, path: "/about", title: t("org.about.title"), description: t("org.about.body") });
}

export default async function About({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return (
    <>
      <h1>{t("org.about.title")}</h1>
      <p>{t("org.about.body")}</p>
      <p>{t("org.about.languages")}</p>
      <a className="button" href={comUrl(locale)}>{t("org.home.cta")}</a>
    </>
  );
}
