import { describe, expect, it } from "vitest";
import fallback from "@/contracts/fallback-en.json";
import { createT, loadMessages } from "./i18n";
import { detectLocale, isLocale, LOCALES } from "./locales";
import { orgUrl } from "./seo";

describe("org lib", () => {
  it("knows the 26 locales and detects from Accept-Language only", () => {
    expect(LOCALES).toHaveLength(26);
    expect(detectLocale("de-DE,de;q=0.9,en;q=0.5")).toBe("de");
    expect(detectLocale("xx")).toBe("bg");
    expect(isLocale("fr")).toBe(true);
  });
  it("falls back to the bundled English dictionary when the API is down", async () => {
    const msgs = await loadMessages("de", (async () => { throw new Error("down"); }) as unknown as typeof fetch);
    expect(createT(msgs)("org.home.title")).toBe((fallback as Record<string, string>)["org.home.title"]);
  });
  it("merges API translations over the fallback", async () => {
    const f = (async () => new Response(JSON.stringify({ "org.home.title": "Hallo" }))) as unknown as typeof fetch;
    const t = createT(await loadMessages("de", f));
    expect(t("org.home.title")).toBe("Hallo");
    expect(t("org.nav.about")).toBe((fallback as Record<string, string>)["org.nav.about"]);
  });
  it("builds canonicals on beeloy.org, not on the community site", () => {
    expect(orgUrl("de", "/about")).toMatch(/^https?:\/\/[^/]+\/de\/about$/);
  });
});
