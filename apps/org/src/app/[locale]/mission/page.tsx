import type { Metadata } from "next";
import { createT, loadMessages } from "@/lib/i18n";
import { buildMetadata, comUrl } from "@/lib/seo";

const SECTIONS = [1, 2, 3, 4, 5, 6];

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return buildMetadata({ locale, path: "/mission", title: t("org.mission.title"), description: t("org.mission.subtitle") });
}

export default async function Mission({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return (
    <>
      <h1>{t("org.mission.title")}</h1>
      <p className="lead">{t("org.mission.subtitle")}</p>
      {SECTIONS.map((n) => (
        <section key={n}>
          <h2>{t(`org.mission.s${n}.title`)}</h2>
          {t(`org.mission.s${n}.body`).split("\n\n").map((para, i) => <p key={i}>{para}</p>)}
        </section>
      ))}
      <h2>{t("org.mission.closing")}</h2>
      <p><strong>{t("org.mission.motto")}</strong></p>
      <a className="button" href={comUrl(locale)}>{t("org.home.cta")}</a>
    </>
  );
}
