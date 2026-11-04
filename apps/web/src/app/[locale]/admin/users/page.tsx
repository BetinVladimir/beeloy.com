import type { Metadata } from "next";
import { UsersTable } from "@/components/Staff";
import { buildMetadata } from "@/lib/seo";

export async function generateMetadata({ params }: { params: Promise<{ locale: string }> }): Promise<Metadata> {
  const { locale } = await params;
  return buildMetadata({ locale, path: "/admin/users", title: "Admin", description: "Admin", noindex: true });
}
export default function Page() { return <UsersTable />; }
