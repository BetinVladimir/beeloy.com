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

const COMPOSE_NET = process.env.COMPOSE_NET ?? "beeloy_internal";
const S3_ALIAS = process.env.E2E_S3_ALIAS ?? "http://dev:dev-minio-secret@s3:9000";
const MC_IMAGE = "cgr.dev/chainguard/minio-client:latest";

/** Runs `mc` (MinIO client) on the compose network against the dev MinIO; `input` is piped to stdin. */
export function mc(args: string[], input?: string): string {
  return execFileSync("docker", ["run", "--rm", ...(input !== undefined ? ["-i"] : []), "--network", COMPOSE_NET, "-e", `MC_HOST_l=${S3_ALIAS}`, MC_IMAGE, ...args], { input, encoding: "utf8" });
}

/** SQL against the dev Postgres container. Returns the raw output. */
export function psql(sql: string): string {
  return execFileSync("docker", ["exec", "beeloy-postgres-1", "psql", "-U", "beeloy", "-tA", "-c", sql], { encoding: "utf8" });
}

export function grantRole(userId: string, role: "admin" | "moderator" | "editor"): void {
  psql(`INSERT INTO role_bindings (id, user_id, role) VALUES (gen_random_uuid(), '${userId}', '${role}') ON CONFLICT DO NOTHING`);
}

/** Calls the API through the page's own session and CSRF header (like the web app does). */
export async function call<T = any>(page: Page, method: string, path: string, body?: unknown): Promise<{ status: number; data: T; raw: any }> {
  return page.evaluate(
    async ({ method, path, body }) => {
      const r = await fetch(path, { method, credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Requested-With": "beeloy" }, body: body === undefined ? undefined : JSON.stringify(body) });
      const raw = await r.json().catch(() => ({}));
      return { status: r.status, data: raw.data, raw };
    },
    { method, path, body },
  );
}

export function containerLogs(name: string): string {
  return execFileSync("docker", ["logs", name], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
}

const FAKE_OAUTH = process.env.FAKE_OAUTH_URL ?? "http://localhost:4010";

/** Chooses who the local stand-in for Google/Facebook (infra/dev/fake-oauth) signs in next. */
export async function providerSignsIn(who: { sub: string; email?: string; name?: string } | { deny: true }): Promise<void> {
  const r = await fetch(`${FAKE_OAUTH}/__identity`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(who) });
  if (!r.ok) throw new Error(`fake-oauth: ${r.status}`);
}

export async function signOut(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Account" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByRole("link", { name: /Sign in|Log in/i }).first()).toBeVisible();
}
