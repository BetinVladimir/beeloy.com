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

describe("legal links and mission", async () => {
  const { legalUrl } = await import("./legal");
  it("links Terms/Privacy/Cookies to the legal site and falls back to English for unsupported languages", () => {
    expect(legalUrl("de", "terms")).toBe("https://legal.beeloy.org/de/terms");
    expect(legalUrl("uk", "privacy")).toBe("https://legal.beeloy.org/en/privacy");
    expect(legalUrl("en", "rules")).toBeNull();
  });
  it("has the whole mission in the reference dictionary", () => {
    const f = fallback as Record<string, string>;
    for (let n = 1; n <= 6; n++) expect(f[`org.mission.s${n}.title`] && f[`org.mission.s${n}.body`]).toBeTruthy();
    expect(f["org.mission.motto"]).toContain("Trust has to be earned");
  });
});
