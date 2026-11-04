import type { Metadata } from "next";
import { SettingsTabs } from "@/components/SettingsTabs";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "/settings", title: t("settings.title"), description: t("settings.title"), noindex: true });
}

export default function Page() {
  return <SettingsTabs />;
}
