import type { Metadata } from "next";
import { ModArticleReview } from "@/components/Staff";
import { buildMetadata } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string; id: string }> }): Promise<Metadata> {
  const { locale, id } = await params;
  return buildMetadata({ locale, path: `/mod/articles/${id}`, title: "Moderation", description: "Moderation", noindex: true });
}
export default async function Page({ params }: { params: Promise<{ id: string }> }) { const { id } = await params; return <ModArticleReview id={id} />; }
