import type { Metadata } from "next";
import { EditorPage } from "@/components/Staff";
import { buildMetadata } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  return buildMetadata({ locale, path: "/editor", title: "Editor", description: "Editor", noindex: true });
}
export default function Page() { return <EditorPage />; }
