import { createHash, randomUUID } from "node:crypto";
import { execFileSync } from "node:child_process";
import { expect, test } from "@playwright/test";
import { call, containerLogs, grantRole, mc, newUser } from "./helpers";

const NEWS = "0198f000-0000-7000-8000-000000000001";

// Articles on the live stack: API -> MinIO (conditional writes) -> Content Sync -> API -> pages.
test("articles: editor flow writes immutable files to MinIO, publish, tamper detection, direct upload", async ({ browser }) => {
  test.setTimeout(240_000);
  const admin = await browser.newContext();
  const { page } = await newUser(admin, "staff", `staff-${Date.now().toString(36)}`);
  await page.goto("/en");
  const me = await call<{ id: string }>(page, "GET", "/api/v1/me");
  grantRole(me.data.id, "admin");

  // --- editor flow through the API ---
  const stamp = Date.now().toString(36);
  const title = `Live article ${stamp}`;
  const created = await call<{ id: string; slug: string }>(page, "POST", "/api/v1/editor/articles", { category_id: NEWS, source_locale: "en", title_for_slug: title });
  expect(created.status, JSON.stringify(created.raw)).toBe(201);
  const { id, slug } = created.data;
  const body = "The first paragraph of the live article, long enough to count as a body.";
  expect((await call(page, "PUT", `/api/v1/editor/articles/${id}/source`, { title, summary: "A summary of the live article.", body_md: body })).status).toBe(200);
  expect((await call(page, "POST", `/api/v1/editor/articles/${id}/submit`)).status).toBe(200);
  const pub = await call(page, "POST", `/api/v1/mod/articles/${id}/publish`, { rev: 1, visibility: "public" });
  expect(pub.status, JSON.stringify(pub.raw)).toBe(200);

  // the files really are in MinIO
  const listing = mc(["ls", "-r", `l/beeloy/articles/${id}/`]);
  expect(listing).toContain("manifest.json");
  expect(listing).toContain("rev-1/en.json");

  // anonymous visitors see the published article, the list and the page
  const anon = await browser.newContext();
  const ap = await anon.newPage();
  await ap.goto(`/en/news/${slug}`);
  await expect(ap.getByRole("heading", { name: title })).toBeVisible();
  await ap.goto("/en/news");
  await expect(ap.getByText(title)).toBeVisible();

  // --- the file of a registered revision is replaced in the bucket (tampering) ---
  const key = `l/beeloy/articles/${id}/rev-1/en.json`;
  const original = mc(["cat", key]);
  const file = JSON.parse(original);
  file.title = "TAMPERED";
  file.body_md = "TAMPERED body that nobody approved";
  mc(["pipe", key], JSON.stringify(file));
  await expect
    .poll(async () => (await call<unknown[]>(page, "GET", "/api/v1/admin/audit?action=content.integrity_violation&limit=50")).data?.some?.((e: any) => e.target_id === id), { timeout: 60_000, intervals: [2000] })
    .toBe(true);
  expect(containerLogs("beeloy-content-sync-1")).toContain("INTEGRITY VIOLATION");
  await ap.goto(`/en/news/${slug}`);
  await expect(ap.getByRole("heading", { name: title })).toBeVisible();
  await expect(ap.getByText("TAMPERED")).toHaveCount(0);
  mc(["pipe", key], original); // put the approved bytes back so that later backup checks see a consistent bucket

  // --- a file uploaded straight to the bucket is registered, hidden until a moderator publishes it ---
  const uid = randomUUID();
  const upSlug = `uploaded-${stamp}`;
  const now = new Date().toISOString();
  mc(["pipe", `l/beeloy/articles/${uid}/rev-1/en.json`], JSON.stringify({ schema_version: 1, article_id: uid, locale: "en", rev: 1, origin: "human", model: null, title: `Uploaded ${stamp}`, summary: "Uploaded summary text.", body_md: "Uploaded body text.", generated_at: now }));
  mc(["pipe", `l/beeloy/articles/${uid}/manifest.json`], JSON.stringify({ schema_version: 1, article_id: uid, slug: upSlug, category_key: "news", source_locale: "en", rev: 1, cover_url: null, author_id: null, created_at: now, updated_at: now }));
  await expect
    .poll(async () => (await call<any[]>(page, "GET", "/api/v1/editor/articles?limit=50")).data?.some?.((a: any) => a.id === uid), { timeout: 60_000, intervals: [2000] })
    .toBe(true);
  expect((await ap.request.get(`/api/v1/articles/${upSlug}?locale=en`)).status()).toBe(404);
  expect((await call(page, "POST", `/api/v1/mod/articles/${uid}/publish`, { rev: 1, visibility: "public" })).status).toBe(200);
  await ap.goto(`/en/news/${upSlug}`);
  await expect(ap.getByRole("heading", { name: `Uploaded ${stamp}` })).toBeVisible();
  await admin.close();
  await anon.close();
});

const BLOG = "0198f000-0000-7000-8000-000000000004";

// The business blog is its own section: list, post URL, redirect from the news URL, RSS, and absence from the news list.
test("blog: article category lives under /articles, not under /news", async ({ browser }) => {
  test.setTimeout(120_000);
  const admin = await browser.newContext();
  const { page } = await newUser(admin, "blogger", `blogger-${Date.now().toString(36)}`);
  await page.goto("/en");
  grantRole((await call<{ id: string }>(page, "GET", "/api/v1/me")).data.id, "admin");
  const title = `Blog post ${Date.now().toString(36)}`;
  const created = await call<{ id: string; slug: string }>(page, "POST", "/api/v1/editor/articles", { category_id: BLOG, source_locale: "en", title_for_slug: title });
  expect(created.status, JSON.stringify(created.raw)).toBe(201);
  const { id, slug } = created.data;
  expect((await call(page, "PUT", `/api/v1/editor/articles/${id}/source`, { title, summary: "A post of the business blog.", body_md: "Body of the blog post, long enough." })).status).toBe(200);
  expect((await call(page, "POST", `/api/v1/editor/articles/${id}/submit`)).status).toBe(200);
  expect((await call(page, "POST", `/api/v1/mod/articles/${id}/publish`, { rev: 1, visibility: "public" })).status).toBe(200);

  const anon = await browser.newContext();
  const ap = await anon.newPage();
  await expect.poll(async () => (await ap.request.get(`/api/v1/articles/${slug}?locale=en`)).status(), { timeout: 60_000, intervals: [2000] }).toBe(200);
  await ap.goto("/en/articles");
  await expect(ap.getByRole("link", { name: title })).toBeVisible();
  await ap.goto("/en/news");
  await expect(ap.getByText(title)).toHaveCount(0);
  await ap.goto(`/en/news/${slug}`); // the news URL of a blog post redirects to its own section
  await expect(ap).toHaveURL(new RegExp(`/en/articles/${slug}$`));
  await expect(ap.getByRole("heading", { name: title })).toBeVisible();
  expect(await ap.locator('script[type="application/ld+json"]').first().textContent()).toContain("BlogPosting");
  expect(await (await ap.request.get("/en/articles/feed.xml")).text()).toContain(`/en/articles/${slug}`);
  await admin.close();
  await anon.close();
});

// Dictionaries on the live stack: a new release is picked up, a rollback needs the S3 manifest to point at the target,
// and polling does not undo it afterwards.
test("dictionaries: new release, rollback conflict, rollback after restoring the manifest, polling keeps it", async ({ request }) => {
  test.setTimeout(240_000);
  const version = async () => ((await (await request.get("/api/v1/i18n/meta")).json()) as { data: { version: string } }).data.version;
  const rollback = (v: string) => {
    try {
      return { ok: true, out: execFileSync("docker", ["exec", "beeloy-content-sync-1", "/content-sync", "rollback", v], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }) };
    } catch (e: any) {
      return { ok: false, out: String(e.stderr ?? "") + String(e.stdout ?? "") };
    }
  };
  const base = "seed-template";
  await expect.poll(version, { timeout: 60_000 }).toBe(base);
  const manifestBase = mc(["cat", "l/beeloy/i18n/manifest.json"]);
  const codes = (JSON.parse(manifestBase).locales as { code: string }[]).map((l) => l.code);
  const v2 = `e2e-${Date.now().toString(36)}`;

  // publish a second release: every locale with a changed title, manifest last
  const locales = [];
  for (const code of codes) {
    const dict = JSON.parse(mc(["cat", `l/beeloy/i18n/releases/${base}/${code}.json`]));
    dict["login.title"] = `Release ${v2}`;
    const raw = JSON.stringify(dict);
    mc(["pipe", `l/beeloy/i18n/releases/${v2}/${code}.json`], raw);
    locales.push({ code, file: `releases/${v2}/${code}.json`, sha256: createHash("sha256").update(raw).digest("hex"), size: Buffer.byteLength(raw) });
  }
  const m2 = { schema_version: 1, version: v2, published_at: new Date().toISOString(), reference_locale: JSON.parse(manifestBase).reference_locale, locales };
  mc(["pipe", "l/beeloy/i18n/manifest.json"], JSON.stringify(m2));
  await expect.poll(version, { timeout: 90_000, intervals: [2000] }).toBe(v2);
  expect(((await (await request.get("/api/v1/i18n/en")).json()) as any)["login.title"] ?? "").toContain(v2);

  // the S3 manifest still names the new release: the rollback is refused with instructions
  const refused = rollback(base);
  expect(refused.ok).toBe(false);
  expect(refused.out).toContain("rollback conflict");
  expect(await version()).toBe(v2);

  // restore the old manifest, then the rollback is accepted and several polls (10 s each) do not undo it
  mc(["pipe", "l/beeloy/i18n/manifest.json"], manifestBase);
  const done = rollback(base);
  expect(done.ok, done.out).toBe(true);
  // the API caches the active version for up to 30 s (i18nstore versionTTL), so allow two periods
  await expect.poll(version, { timeout: 60_000, intervals: [2000] }).toBe(base);
  await new Promise((r) => setTimeout(r, 35_000));
  expect(await version()).toBe(base);
});
