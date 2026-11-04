import "@mantine/core/styles.css";
import "@mantine/notifications/styles.css";
import { Container } from "@mantine/core";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import type { ReactNode } from "react";
import { ColorSchemeScript, Providers } from "@/components/Providers";
import { CookieBanner } from "@/components/CookieBanner";
import { Footer } from "@/components/Footer";
import { Header } from "@/components/Header";
import { createT, loadMessages } from "@/lib/i18n";
import { getDir, isLocale } from "@/lib/locales";

// Content, profiles, Ideas and search must reflect the current state (visibility can change at any moment):
// every page below is rendered per request and nothing is cached by Next.js. Only i18n and categories use a short cache.
export const dynamic = "force-dynamic";

export const metadata: Metadata = { title: { default: "Beeloy", template: "%s · Beeloy" } };

export default async function LocaleLayout({ children, params }: { children: ReactNode; params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  const messages = await loadMessages(locale);
  const t = createT(messages, locale);
  return (
    <html lang={locale} dir={getDir(locale)} suppressHydrationWarning>
      <head>
        <ColorSchemeScript defaultColorScheme="auto" />
      </head>
      <body>
        <Providers locale={locale} messages={messages}>
          <Header />
          <main>
            <Container size="lg" py="lg">{children}</Container>
          </main>
          <Footer t={t} locale={locale} />
          <CookieBanner />
        </Providers>
      </body>
    </html>
  );
}
