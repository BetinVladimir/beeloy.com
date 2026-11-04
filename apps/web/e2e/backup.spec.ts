import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import { call, mc, newUser, psql } from "./helpers";

const NET = process.env.COMPOSE_NET ?? "beeloy_internal";
const ENV = [
  "RESTIC_REPOSITORY=s3:http://s3:9000/backup/restic", "RESTIC_PASSWORD=dev-restic-pass", "AWS_ACCESS_KEY_ID=dev", "AWS_SECRET_ACCESS_KEY=dev-minio-secret",
  "DATABASE_URL=postgres://beeloy:beeloy@postgres:5432/beeloy?sslmode=disable", "REDIS_URL=redis://redis:6379/0",
  "S3_ENDPOINT=http://s3:9000", "S3_REGION=us-east-1", "S3_BUCKET=beeloy", "S3_ACCESS_KEY=dev", "S3_SECRET_KEY=dev-minio-secret", "MAINT_WAIT=8",
];
const dockerArgs = (extra: string[], image: string, cmd: string[]) => ["run", "--rm", "--network", NET, ...ENV.flatMap((e) => ["-e", e]), ...extra, image, ...cmd];

// The backup scripts with real restic + rclone against MinIO, and the deletion journal with replay after a restore.
test("backup freezes writes, restore test passes, a purge after the backup is re-applied from the journal", async ({ browser }) => {
  test.setTimeout(300_000);
  const ctx = await browser.newContext();
  const { page } = await newUser(ctx, "leaver", `leaver-${Date.now().toString(36)}`);
  await page.goto("/en");
  const me = (await call<{ id: string }>(page, "GET", "/api/v1/me")).data;

  // 1. backup: writes are refused with 503 while it runs, reads keep working, the flag is cleared at the end
  const bk = spawn("docker", dockerArgs([], "beeloy-backup", ["backup.sh"]));
  let out = "";
  bk.stdout.on("data", (d) => (out += d));
  bk.stderr.on("data", (d) => (out += d));
  const finished = new Promise<number>((res) => bk.on("close", (c) => res(c ?? 1)));
  await expect.poll(() => out, { timeout: 60_000 }).toContain("writes frozen");
  const frozen = await call(page, "POST", "/api/v1/ideas", { title: "During backup", body: "x".repeat(40), category: "other", locale: "en" });
  expect(frozen.status).toBe(503);
  expect((await call(page, "GET", "/api/v1/me")).status).toBe(200);
  expect(await finished, out).toBe(0);
  expect(out).toContain("backup ok");
  expect((await call(page, "POST", "/api/v1/ideas", { title: "After backup ok", body: "y".repeat(40), category: "other", locale: "en" })).status).toBe(201);

  // 2. the restore test of the latest snapshot (dump + objects + hashes + schema + admin + live index)
  const rt = execFileSync("docker", dockerArgs(["--user", "root", "-v", "/var/run/docker.sock:/var/run/docker.sock", "--entrypoint", "sh"], "beeloy-backup",
    ["-c", "apk add -q --no-cache docker-cli >/dev/null && restore-test.sh"]), { encoding: "utf8" });
  expect(rt).toContain("restore-test: OK");

  // 3. the account is deleted AFTER the backup: grace period over, purge job, journal entry in its own bucket
  expect((await call(page, "DELETE", "/api/v1/me")).status).toBe(202);
  psql(`UPDATE users SET deletion_requested_at = now() - interval '31 days' WHERE id='${me.id}'`);
  const job = execFileSync("docker", ["exec", "beeloy-api-1", "/api", "run-job", "PurgeDeletedAccounts"], { encoding: "utf8" });
  expect(job).toContain("ran=true");
  expect(psql(`SELECT status FROM users WHERE id='${me.id}'`).trim()).toBe("deleted");
  const entry = `l/journal/deletion-journal/${me.id}.enc`;
  expect(mc(["ls", entry])).toContain(`${me.id}.enc`);
  expect(mc(["cat", entry]).includes(me.id)).toBe(false); // encrypted: no plain user id

  // 4. restore the snapshot taken BEFORE the deletion into a fresh database: the account is alive there ...
  const dir = mkdtempSync("/tmp/beeloy-restore-");
  const pg = `beeloy-replay-pg-${Date.now()}`;
  try {
    execFileSync("docker", dockerArgs(["-v", `${dir}:/out`], "beeloy-backup", ["restic", "restore", "latest", "--tag", "beeloy", "--target", "/out"]), { stdio: "ignore" });
    const dump = findFile(dir, "beeloy.dump");
    execFileSync("docker", ["run", "-d", "--name", pg, "--network", NET, "-e", "POSTGRES_PASSWORD=restore", "-e", "POSTGRES_DB=restore", "postgres:17-alpine"], { stdio: "ignore" });
    for (let i = 0; i < 60; i++) {
      try { execFileSync("docker", ["exec", pg, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], { stdio: "ignore" }); break; } catch { await new Promise((r) => setTimeout(r, 1000)); }
    }
    execFileSync("docker", ["exec", "-i", pg, "pg_restore", "-U", "postgres", "-d", "restore", "--no-owner"], { input: readFileSync(dump), stdio: ["pipe", "ignore", "ignore"] });
    const q = (sql: string) => execFileSync("docker", ["exec", pg, "psql", "-U", "postgres", "-d", "restore", "-tAc", sql], { encoding: "utf8" }).trim();
    expect(q(`SELECT status FROM users WHERE id='${me.id}'`)).toBe("active");

    // ... and `api replay-deletions` re-applies the journaled purge before traffic is opened
    const api = execFileSync("docker", ["inspect", "beeloy-api-1", "--format", "{{.Config.Image}}"], { encoding: "utf8" }).trim();
    // same environment as the running API, only the database differs
    const env = (JSON.parse(execFileSync("docker", ["inspect", "beeloy-api-1", "--format", "{{json .Config.Env}}"], { encoding: "utf8" })) as string[])
      .filter((e) => /^[A-Z0-9_]+=/.test(e) && !e.startsWith("DATABASE_URL=") && !e.startsWith("PATH="));
    const replay = execFileSync("docker", ["run", "--rm", "--network", NET, "-e", `DATABASE_URL=postgres://postgres:restore@${pg}:5432/restore?sslmode=disable`,
      ...env.flatMap((e) => ["-e", e]), api, "/api", "replay-deletions"], { encoding: "utf8" });
    expect(replay).toMatch(/deletions re-applied: [1-9]/);
    expect(q(`SELECT status || '/' || nickname FROM users WHERE id='${me.id}'`)).toBe("deleted/Deleted user");
    expect(q(`SELECT count(*) FROM auth_identities WHERE user_id='${me.id}'`)).toBe("0");
  } finally {
    execFileSync("docker", ["rm", "-f", pg], { stdio: "ignore" });
    rmSync(dir, { recursive: true, force: true });
  }
  await ctx.close();
});

function findFile(dir: string, name: string): string {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (e === name) return p;
    if (statSync(p).isDirectory()) {
      try { return findFile(p, name); } catch { /* keep looking */ }
    }
  }
  throw new Error(`${name} not found`);
}
