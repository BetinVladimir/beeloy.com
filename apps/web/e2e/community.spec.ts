import { expect, test } from "@playwright/test";
import { newUser, signIn, uniqueEmail } from "./helpers";

test("1. sign in by e-mail code, change @slug, sign out", async ({ page }) => {
  const email = uniqueEmail("alice");
  await signIn(page, email);
  await page.goto("/en/settings");
  const newSlug = `alice-${Date.now().toString(36)}`;
  await page.getByLabel("Public @name").last().fill(newSlug);
  await page.getByRole("button", { name: "Change @name" }).click();
  await page.goto(`/en/people/${newSlug}`);
  await expect(page.getByText(`@${newSlug}`).first()).toBeVisible();
  await page.getByRole("button", { name: "Account" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await page.goto("/en/messages");
  await expect(page).toHaveURL(/\/en\/login/);
});

test("2. idea: submit, another member votes, comment shows nickname and @slug", async ({ browser }) => {
  const a = await browser.newContext();
  const b = await browser.newContext();
  const { page: pa } = await newUser(a, "author", `author-${Date.now().toString(36)}`);
  const { page: pb } = await newUser(b, "voter", `voter-${Date.now().toString(36)}`);
  const title = `E2E idea ${Date.now()}`;
  await pa.goto("/en/ideas/new");
  await pa.getByLabel(/^Title/).fill(title);
  await pa.getByLabel(/^Description/).fill("A description that is definitely long enough to be accepted by the server.");
  await pa.getByRole("button", { name: "Submit an idea" }).click();
  await expect(pa.getByRole("heading", { name: title })).toBeVisible();
  await pb.goto(pa.url());
  await pb.getByRole("button", { name: /^Vote/ }).click();
  await expect(pb.getByRole("button", { name: /Remove vote/ })).toBeVisible();
  await pb.getByPlaceholder("Write a comment…").fill("Great idea, @author!");
  await pb.getByRole("button", { name: "Post comment" }).click();
  await expect(pb.getByText("Great idea,")).toBeVisible();
  await expect(pb.locator("section").getByRole("link", { name: /^@voter-/ })).toBeVisible();
  await a.close();
  await b.close();
});

test("3. messenger: request, accept, real-time delivery and recovery after being offline", async ({ browser }) => {
  const a = await browser.newContext();
  const b = await browser.newContext();
  const bSlug = `receiver-${Date.now().toString(36)}`;
  const { page: pa } = await newUser(a, "sender", `sender-${Date.now().toString(36)}`);
  const { page: pb } = await newUser(b, "receiver", bSlug);
  await pa.goto(`/en/people/${bSlug}`);
  await pa.getByRole("button", { name: "Message" }).click();
  await expect(pa).toHaveURL(/\/en\/messages\//);
  await pa.getByPlaceholder("Write a message…").fill("Hello from A");
  await pa.keyboard.press("Enter");
  await expect(pa.getByText("Hello from A")).toBeVisible();
  // B sees a request, accepts and answers
  await pb.goto("/en/messages");
  await pb.getByRole("tab", { name: /Requests/ }).click();
  await pb.getByText("Hello from A").click();
  await pb.getByRole("button", { name: "Accept" }).click();
  await pb.getByPlaceholder("Write a message…").fill("Hi A!");
  await pb.keyboard.press("Enter");
  await expect(pa.getByText("Hi A!")).toBeVisible({ timeout: 15_000 }); // delivered live over SSE
  // B goes offline, A writes, B comes back and the message appears
  await b.setOffline(true);
  await pa.getByPlaceholder("Write a message…").fill("You were offline");
  await pa.keyboard.press("Enter");
  await b.setOffline(false);
  await expect(pb.getByText("You were offline")).toBeVisible({ timeout: 40_000 });
  await a.close();
  await b.close();
});
