import type { Metadata } from "next";
import { ReportsTable } from "@/components/Staff";
import { buildMetadata } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  return buildMetadata({ locale, path: "/mod/reports", title: "Reports", description: "Reports", noindex: true });
}
export default function Page() { return <ReportsTable />; }
