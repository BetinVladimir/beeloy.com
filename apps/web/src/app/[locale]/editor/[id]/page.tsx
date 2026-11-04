import type { Metadata } from "next";
import { EditorArticle } from "@/components/Staff";
import { buildMetadata } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string; id: string }> }): Promise<Metadata> {
  const { locale, id } = await params;
  return buildMetadata({ locale, path: `/editor/${id}`, title: "Editor", description: "Editor", noindex: true });
}
export default async function Page({ params }: { params: Promise<{ id: string }> }) { const { id } = await params; return <EditorArticle id={id} />; }
