import { execFileSync } from "node:child_process";
import { expect, type BrowserContext, type Page } from "@playwright/test";

const MAILPIT = process.env.MAILPIT_URL ?? "http://localhost:8025";

/** Reads the newest sign-in code sent to `email` from Mailpit. */
export async function waitForCode(email: string): Promise<string> {
  for (let i = 0; i < 40; i++) {
    const list = (await (await fetch(`${MAILPIT}/api/v1/messages`)).json()) as { messages: { ID: string; To: { Address: string }[] }[] };
    const msg = list.messages.find((m) => m.To.some((t) => t.Address === email));
    if (msg) {
      const full = (await (await fetch(`${MAILPIT}/api/v1/message/${msg.ID}`)).json()) as { Text: string };
      const code = full.Text.match(/\b(\d{6})\b/)?.[1];
      if (code) {
        await fetch(`${MAILPIT}/api/v1/messages`, { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ IDs: [msg.ID] }) });
        return code;
      }
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`no code for ${email}`);
}

export function uniqueEmail(prefix: string): string {
  return `${prefix}-${Date.now()}-${Math.floor(Math.random() * 1e4)}@example.com`;
}

/** Signs in through the real UI (e-mail → code → terms). Returns the public @slug. */
export async function signIn(page: Page, email: string, slug?: string): Promise<void> {
  await page.goto("/en/login");
  await page.getByLabel(/^Email/).fill(email);
  await page.getByRole("button", { name: "Send code" }).click();
  await expect(page.getByText(/We sent a 6-digit code/)).toBeVisible();
  const code = await waitForCode(email);
  if (slug) {
    await page.getByRole("button", { name: /Choose your public name/ }).click();
    await page.getByLabel("Public @name").fill(slug);
  }
  await page.getByRole("checkbox").check();
  await page.getByLabel("Code from the email").first().click();
  await page.keyboard.type(code);
  // the sixth digit submits the form by itself (terms are accepted); clicking Confirm too would race with that request
  await expect(page).toHaveURL(/\/en$/);
}

export async function newUser(context: BrowserContext, prefix: string, slug?: string) {
  const page = await context.newPage();
  const email = uniqueEmail(prefix);
  await signIn(page, email, slug);
  return { page, email };
}

/** Gives the first administrator rights through the CLI of the running container (ignored if one exists). */
export function bootstrapAdmin(email: string): void {
  try {
    execFileSync("docker", ["compose", "-f", "../../infra/compose/docker-compose.yml", "-f", "../../infra/compose/compose.dev.yml", "exec", "-T", "api", "/api", "bootstrap-admin", "--email", email], { stdio: "ignore", env: { ...process.env, POSTGRES_PASSWORD: "beeloy" } });
  } catch {
    /* an administrator already exists */
  }
}
