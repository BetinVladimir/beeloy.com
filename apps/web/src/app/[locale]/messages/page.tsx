import type { Metadata } from "next";
import { ChatPage } from "@/components/Chat";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "/messages", title: t("messages.title"), description: t("messages.title"), noindex: true });
}

export default function Page() {
  return <ChatPage />;
}
