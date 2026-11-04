import type { Metadata } from "next";
import { notFound, redirect } from "next/navigation";
import { ApiError, api } from "@/lib/api";
import { isLocale } from "@/lib/locales";

export const metadata: Metadata = { robots: { index: false, follow: false } };

// Permanent link by UUID: resolves to the canonical address of the resource.
export default async function Page({ params }: { params: Promise<{ locale: string; id: string }> }) {
  const { locale, id } = await params;
  if (!isLocale(locale)) notFound();
  let path: string | null = null;
  try {
    path = (await api<{ canonical_path: string }>(`/api/v1/resources/${id}`, { cache: "no-store" })).data.canonical_path;
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
  }
  if (!path) notFound();
  redirect(`/${locale}${path}`);
}
