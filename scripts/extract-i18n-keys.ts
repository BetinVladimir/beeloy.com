// Collects every t("key") used by apps/web, writes apps/web/src/contracts/i18n-keys.json and
// fails when a key is missing from contracts/messages/en.json (the source dictionary for translators).
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const src = join(root, "apps/web/src");
const en: Record<string, string> = JSON.parse(readFileSync(join(root, "contracts/messages/en.json"), "utf8"));

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) return walk(p);
    return /\.(ts|tsx)$/.test(f) && !/\.test\.(ts|tsx)$/.test(f) ? [p] : [];
  });
}

const literal = /\bt\(\s*["']([a-z0-9_.]+)["']/g;
const template = /\bt\(\s*`([a-z0-9_.]*)\$\{/g;
const keys = new Set<string>();
const prefixes = new Set<string>();
for (const file of walk(src)) {
  const text = readFileSync(file, "utf8");
  for (const m of text.matchAll(literal)) keys.add(m[1]);
  for (const m of text.matchAll(template)) prefixes.add(m[1]);
}

const missing = [...keys].filter((k) => !(k in en) && !(`${k}.other` in en));
const missingPrefix = [...prefixes].filter((p) => !Object.keys(en).some((k) => k.startsWith(p)));
writeFileSync(join(src, "contracts/i18n-keys.json"), JSON.stringify({ keys: [...keys].sort(), dynamic_prefixes: [...prefixes].sort() }, null, 2) + "\n");

if (process.argv.includes("--list")) console.log([...keys].sort().join("\n"));
if (missing.length || missingPrefix.length) {
  console.error("Missing in contracts/messages/en.json:\n" + [...missing, ...missingPrefix.map((p) => p + "*")].sort().join("\n"));
  process.exit(1);
}
console.log(`ok: ${keys.size} keys, ${prefixes.size} dynamic prefixes`);
