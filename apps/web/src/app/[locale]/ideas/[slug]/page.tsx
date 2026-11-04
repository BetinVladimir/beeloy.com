import type { Metadata } from "next";
import { notFound, permanentRedirect } from "next/navigation";
import { IdeaDetail } from "@/components/IdeaDetail";
import type { HistoryItem, Idea } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { isLocale } from "@/lib/locales";
import { buildMetadata } from "@/lib/seo";

async function load(slug: string) {
  try {
    const r = await api<{ idea: Idea; history: HistoryItem[] }>(`/api/v1/ideas/${slug}`, { cache: "no-store" });
    return { ...r.data, canonical: String(r.raw.canonical_slug ?? slug) };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export async function generateMetadata({ params }: { params: Promise<{ locale: string; slug: string }> }): Promise<Metadata> {
  const { locale, slug } = await params;
  const r = await load(slug);
  if (!r) return { robots: { index: false } };
  return buildMetadata({ locale, path: `/ideas/${r.canonical}`, title: r.idea.title, description: r.idea.body.slice(0, 160) });
}

export default async function Page({ params }: { params: Promise<{ locale: string; slug: string }> }) {
  const { locale, slug } = await params;
  if (!isLocale(locale)) notFound();
  const r = await load(slug);
  if (!r) notFound();
  if (r.canonical !== slug) permanentRedirect(`/${locale}/ideas/${r.canonical}`);
  return <IdeaDetail initial={r.idea} history={r.history} />;
}
