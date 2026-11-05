/** The business blog lives in its own section; everything else is shown under news. */
export const BLOG_CATEGORY = "article";

export type Section = "news" | "articles";

export function sectionOf(categoryKey: string): Section {
  return categoryKey === BLOG_CATEGORY ? "articles" : "news";
}

export function articlePath(locale: string, categoryKey: string, slug: string): string {
  return `/${locale}/${sectionOf(categoryKey)}/${slug}`;
}
