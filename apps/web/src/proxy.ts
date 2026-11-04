import { NextResponse, type NextRequest } from "next/server";
import { detectLocale, isLocale } from "@/lib/locales";

/** Unprefixed paths get a locale: cookie bl_locale → Accept-Language → default. Prefixed URLs are never rewritten. */
export function proxy(req: NextRequest) {
  const { pathname, search } = req.nextUrl;
  const first = pathname.split("/")[1];
  if (isLocale(first)) return NextResponse.next();
  const locale = detectLocale(req.cookies.get("bl_locale")?.value, req.headers.get("accept-language"));
  const target = new URL(`/${locale}${pathname === "/" ? "" : pathname}${search}`, req.url);
  const res = NextResponse.redirect(target, 307);
  res.headers.set("Cache-Control", "private, no-store"); // the target depends on cookie and Accept-Language
  return res;
}

export const config = {
  matcher: ["/((?!api/|_next/|.*\\..*).*)"],
};
