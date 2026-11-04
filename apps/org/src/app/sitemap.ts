import type { MetadataRoute } from "next";
import { LOCALES } from "@/lib/locales";
import { orgUrl } from "@/lib/seo";

const PATHS = ["", "/about", "/mission", "/rules", "/contact"];

export default function sitemap(): MetadataRoute.Sitemap {
  return LOCALES.flatMap((l) => PATHS.map((p) => ({ url: orgUrl(l, p), changeFrequency: "monthly" as const })));
}
