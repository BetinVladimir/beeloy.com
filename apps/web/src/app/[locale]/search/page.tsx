import type { Metadata } from "next";
import { Suspense } from "react";
import { SearchPage } from "@/components/SearchPage";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "/search", title: t("search.title"), description: t("search.title"), noindex: true });
}

export default function Page() {
  return <Suspense><SearchPage /></Suspense>;
}
