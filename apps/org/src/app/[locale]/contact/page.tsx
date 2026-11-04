import type { Metadata } from "next";
import { createT, loadMessages } from "@/lib/i18n";
import { buildMetadata } from "@/lib/seo";

// TODO-CONTENT: the real contact address is supplied by the owner (CONTACT_EMAIL); until then a placeholder is shown.
const CONTACT_EMAIL = process.env.CONTACT_EMAIL ?? "TODO-CONTACT@beeloy.org";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return buildMetadata({ locale, path: "/contact", title: t("org.contact.title"), description: t("org.contact.body") });
}

export default async function Contact({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return (
    <>
      <h1>{t("org.contact.title")}</h1>
      <p>{t("org.contact.body")}</p>
      <p>{t("org.contact.email_label")}: <a href={`mailto:${CONTACT_EMAIL}`}>{CONTACT_EMAIL}</a></p>
    </>
  );
}
