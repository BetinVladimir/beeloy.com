import "../globals.css";
import type { Metadata } from "next";
import Link from "next/link";
import { legalUrl } from "@/lib/legal";
import { notFound } from "next/navigation";
import type { ReactNode } from "react";
import { createT, loadMessages } from "@/lib/i18n";
import { getDir, isLocale } from "@/lib/locales";
import { comUrl } from "@/lib/seo";

export const metadata: Metadata = { title: { default: "Beeloy", template: "%s · Beeloy" } };

export default async function Layout({ children, params }: { children: ReactNode; params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  const t = createT(await loadMessages(locale));
  return (
    <html lang={locale} dir={getDir(locale)}>
      <body>
        <header>
          <div className="wrap">
            <Link href={`/${locale}`} className="brand">{t("org.brand")}</Link>
            <nav aria-label="Main">
              <Link href={`/${locale}/about`}>{t("org.nav.about")}</Link>
              <Link href={`/${locale}/mission`}>{t("org.nav.mission")}</Link>
              <Link href={`/${locale}/rules`}>{t("org.nav.rules")}</Link>
              <Link href={`/${locale}/contact`}>{t("org.nav.contact")}</Link>
              <a href={comUrl(locale)}>{t("org.nav.community")}</a>
            </nav>
          </div>
        </header>
        <main><div className="wrap">{children}</div></main>
        <footer>
          <div className="wrap">
            <span>© Beeloy · {t("org.footer.note")}</span>
            <span>
              <a href={legalUrl(locale, "terms")!}>{t("legal.terms.title")}</a>{" · "}
              <a href={legalUrl(locale, "privacy")!}>{t("legal.privacy.title")}</a>{" · "}
              <a href={legalUrl(locale, "cookies")!}>{t("legal.cookies.title")}</a>{" · "}
              <a href={comUrl(locale)}>{t("org.footer.com")}</a>
            </span>
          </div>
        </footer>
      </body>
    </html>
  );
}
