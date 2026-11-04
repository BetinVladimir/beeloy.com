import type { Metadata } from "next";
import { ModArticleQueue } from "@/components/Staff";
import { buildMetadata } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  return buildMetadata({ locale, path: "/mod/articles", title: "Moderation", description: "Moderation", noindex: true });
}
export default function Page() { return <ModArticleQueue />; }
