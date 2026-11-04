import type { Metadata } from "next";
import { createT, loadMessages } from "@/lib/i18n";
import { buildMetadata, comUrl } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return buildMetadata({ locale, path: "/rules", title: t("org.rules.title"), description: t("org.rules.item1") });
}

export default async function Rules({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return (
    <>
      <h1>{t("org.rules.title")}</h1>
      <ul>
        {[1, 2, 3, 4].map((n) => <li key={n}>{t(`org.rules.item${n}`)}</li>)}
      </ul>
      <p><a href={comUrl(locale, "/legal/rules")}>{t("org.rules.full")}</a></p>
    </>
  );
}
