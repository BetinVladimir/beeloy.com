import type { Metadata } from "next";
import { LoginForm } from "@/components/LoginForm";
import { buildMetadata } from "@/lib/seo";
import { getT } from "@/lib/server";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  const t = await getT(locale);
  return buildMetadata({ locale, path: "/login", title: t("login.title"), description: t("login.title"), noindex: true });
}

export default function Page() {
  return <LoginForm />;
}
