import type { Metadata } from "next";
import { createT, loadMessages } from "@/lib/i18n";
import { buildMetadata, comUrl } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return buildMetadata({ locale, path: "", title: t("org.home.title"), description: t("org.home.lead") });
}

export default async function Home({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  const t = createT(await loadMessages(locale));
  return (
    <>
      <h1>{t("org.home.title")}</h1>
      <p className="lead">{t("org.home.lead")}</p>
      <a className="button" href={comUrl(locale)}>{t("org.home.cta")}</a>
      <div className="points">
        {[1, 2, 3].map((n) => (
          <section key={n}>
            <h2>{t(`org.home.point${n}.title`)}</h2>
            <p>{t(`org.home.point${n}.text`)}</p>
          </section>
        ))}
      </div>
    </>
  );
}
