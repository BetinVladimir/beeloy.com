import type { Metadata } from "next";
import { notFound, permanentRedirect } from "next/navigation";
import { PeopleProfile } from "@/components/PeopleProfile";
import type { PublicProfile } from "@/contracts/api-types";
import { ApiError, api } from "@/lib/api";
import { isLocale } from "@/lib/locales";
import { buildMetadata } from "@/lib/seo";

async function load(slug: string) {
  try {
    return (await api<PublicProfile>(`/api/v1/users/${slug}`, { cache: "no-store" })).data;
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export async function generateMetadata({ params }: { params: Promise<{ locale: string; slug: string }> }): Promise<Metadata> {
  const { locale, slug } = await params;
  const p = await load(slug);
  if (!p) return { robots: { index: false } };
  return buildMetadata({ locale, path: `/people/${p.canonical_slug}`, title: `${p.nickname} (@${p.canonical_slug})`, description: p.bio || `@${p.canonical_slug}`, noindex: !p.activity_visible });
}

export default async function Page({ params }: { params: Promise<{ locale: string; slug: string }> }) {
  const { locale, slug } = await params;
  if (!isLocale(locale)) notFound();
  const p = await load(slug);
  if (!p) notFound();
  if (p.canonical_slug !== slug) permanentRedirect(`/${locale}/people/${p.canonical_slug}`);
  return <PeopleProfile profile={p} />;
}
