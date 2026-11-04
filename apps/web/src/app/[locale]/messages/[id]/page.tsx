import type { Metadata } from "next";
import { ChatView } from "@/components/Chat";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string; id: string }> }): Promise<Metadata> {
  const { locale, id } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: `/messages/${id}`, title: t("messages.title"), description: t("messages.title"), noindex: true });
}

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return <ChatView id={id} />;
}
