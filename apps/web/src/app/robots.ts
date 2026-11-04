import type { MetadataRoute } from "next";
import { LOCALES } from "@/lib/locales";
import { SITE_URL } from "@/lib/seo";

export default function robots(): MetadataRoute.Robots {
  const priv = ["login", "settings", "messages", "editor", "mod", "admin", "search", "ideas/new"];
  return {
    rules: [{ userAgent: "*", allow: "/", disallow: ["/api/", ...LOCALES.flatMap((l) => priv.map((p) => `/${l}/${p}`))] }],
    sitemap: `${SITE_URL}/sitemap.xml`,
  };
}
