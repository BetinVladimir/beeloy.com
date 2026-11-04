import { NextResponse, type NextRequest } from "next/server";
import { detectLocale, isLocale } from "@/lib/locales";

/** Unprefixed paths get a locale from Accept-Language (default bg). No cookies are read or set. */
export function proxy(req: NextRequest) {
  const { pathname, search } = req.nextUrl;
  if (isLocale(pathname.split("/")[1])) return NextResponse.next();
  const locale = detectLocale(req.headers.get("accept-language"));
  const res = NextResponse.redirect(new URL(`/${locale}${pathname === "/" ? "" : pathname}${search}`, req.url), 307);
  res.headers.set("Cache-Control", "private, no-store");
  return res;
}

export const config = { matcher: ["/((?!_next/|.*\\..*).*)"] };
