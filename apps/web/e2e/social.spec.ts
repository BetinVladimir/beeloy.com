import { expect, test, type Page } from "@playwright/test";
import { call, newUser, providerSignsIn, signIn, signOut, uniqueEmail } from "./helpers";

const uniq = () => `${Date.now().toString(36)}${Math.floor(Math.random() * 1e4).toString(36)}`;

async function viaProvider(page: Page, name: "Google" | "Facebook") {
  await page.goto("/en/login");
  await page.getByRole("link", { name: `Continue with ${name}` }).click();
}

// Google/Facebook flow against the local stand-in provider: real redirects, cookies, state/PKCE, userinfo.
test.describe("social sign-in (stand-in provider)", () => {
  test("new account: onboarding with terms, neutral public name, then plain sign-in next time", async ({ page }) => {
    const sub = `g-${uniq()}`;
    await providerSignsIn({ sub, email: `gina-${uniq()}@example.com`, name: "Gina Google" });
    await viaProvider(page, "Google");
    await expect(page).toHaveURL(/\/en\/login\?step=onboarding/);
    // the terms must be accepted before the account exists
    await expect(page.getByRole("button", { name: "Confirm" })).toBeDisabled();
    expect((await call(page, "GET", "/api/v1/me")).status).toBe(401);
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page).toHaveURL(/\/en$/);
    const me = (await call<{ id: string; nickname: string; slug: string }>(page, "GET", "/api/v1/me")).data;
    expect(me.nickname).toBe("User");               // not the provider's name, not the e-mail
    expect(me.slug).toMatch(/^user-[a-z0-9]{8}$/);   // random, not derived from anything private
    await signOut(page);
    // the same provider identity signs in directly
    await viaProvider(page, "Google");
    await expect(page).toHaveURL(/\/en$/);
    expect((await call<{ id: string }>(page, "GET", "/api/v1/me")).data.id).toBe(me.id);
  });

  test("a chosen public name is used at onboarding", async ({ page }) => {
    await providerSignsIn({ sub: `f-${uniq()}`, name: "Fiona Facebook" });
    await viaProvider(page, "Facebook");
    await page.getByRole("button", { name: /Choose your public name/ }).click();
    const slug = `fiona-${uniq()}`.slice(0, 30);
    await page.getByLabel("Public @name").fill(slug);
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page).toHaveURL(/\/en$/);
    expect((await call<{ slug: string }>(page, "GET", "/api/v1/me")).data.slug).toBe(slug);
  });

  test("a provider with the same e-mail is a different account (never merged by e-mail)", async ({ browser }) => {
    const email = uniqueEmail("same");
    const a = await browser.newContext();
    const pa = await a.newPage();
    await signIn(pa, email);
    const first = (await call<{ id: string }>(pa, "GET", "/api/v1/me")).data.id;
    const b = await browser.newContext();
    const pb = await b.newPage();
    await providerSignsIn({ sub: `g-${uniq()}`, email, name: "Same Mail" });
    await viaProvider(pb, "Google");
    await expect(pb).toHaveURL(/step=onboarding/); // not signed in as the e-mail account
    await pb.getByRole("checkbox").check();
    await pb.getByRole("button", { name: "Confirm" }).click();
    await expect(pb).toHaveURL(/\/en$/);
    expect((await call<{ id: string }>(pb, "GET", "/api/v1/me")).data.id).not.toBe(first);
    await a.close();
    await b.close();
  });

  test("a denied or failed provider flow shows an error and signs nobody in", async ({ page }) => {
    await providerSignsIn({ deny: true });
    await viaProvider(page, "Google");
    await expect(page).toHaveURL(/error=oauth/);
    await expect(page.getByRole("alert").first()).toBeVisible();
    expect((await call(page, "GET", "/api/v1/me")).status).toBe(401);
  });

  test("link and unlink a provider in the settings; the last sign-in method cannot be removed", async ({ browser }) => {
    const ctx = await browser.newContext();
    const { page } = await newUser(ctx, "linker", `linker-${uniq()}`.slice(0, 30));
    await page.goto("/en/settings");
    await page.getByRole("tab", { name: /Sign-in methods|Login methods/i }).click();
    await providerSignsIn({ sub: `g-${uniq()}`, name: "Linked Google" });
    const callback = page.waitForResponse((r) => r.url().includes("/api/v1/auth/oauth/google/callback"));
    await page.getByRole("button", { name: /Link.*Google|Google/ }).first().click();
    await callback;
    await page.waitForLoadState("load");
    await page.goto("/en/settings");
    await page.getByRole("tab", { name: /Sign-in methods|Login methods/i }).click();
    const list = (await call<{ id: string; provider: string }[]>(page, "GET", "/api/v1/me/identities")).data;
    expect(list.map((i) => i.provider).sort()).toEqual(["email_otp", "google"]);
    const google = list.find((i) => i.provider === "google")!;
    const email = list.find((i) => i.provider === "email_otp")!;
    expect((await call(page, "DELETE", `/api/v1/me/identities/${google.id}`)).status).toBe(204);
    expect((await call(page, "DELETE", `/api/v1/me/identities/${email.id}`)).status).toBe(409); // last method
    await ctx.close();
  });
});
