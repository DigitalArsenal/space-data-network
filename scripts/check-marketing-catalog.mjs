#!/usr/bin/env node
// Checks docs/marketing/catalog.json, the single source of
// spacedatanetwork.org/marketing.html, against itself and against the pinned
// SpaceAware console:
//   - ids are unique, and every reference (category, access, delivery,
//     bundle member) resolves;
//   - every module sits in at least one bundle;
//   - no bundle shows a price unless an approved price list is named;
//   - every console mode is either covered by a module or listed in notListed
//     with a reason, and every MODE:CARD key the catalog names still exists.
// A new console mode therefore cannot land without a catalog decision.
//
//   node scripts/check-marketing-catalog.mjs [--console <spaceaware-ui dir>]
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const consoleArg = args.indexOf("--console");
const consoleDir = resolve(consoleArg >= 0 ? args[consoleArg + 1] : join(root, "sdn-js/spaceaware-ui"));
const catalogPath = join(root, "docs/marketing/catalog.json");
const errors = [];
const fail = (message) => errors.push(message);

const catalog = JSON.parse(readFileSync(catalogPath, "utf8"));

function unique(list, what) {
  const seen = new Set();
  for (const entry of list) {
    if (!entry.id) fail(`${what} without an id`);
    else if (seen.has(entry.id)) fail(`duplicate ${what} id ${entry.id}`);
    seen.add(entry.id);
  }
  return seen;
}

const categories = unique(catalog.categories, "category");
const modules = unique(catalog.modules, "module");
unique(catalog.bundles, "bundle");

for (const m of catalog.modules) {
  if (!categories.has(m.category)) fail(`module ${m.id}: unknown category ${m.category}`);
  if (!(m.access in catalog.access)) fail(`module ${m.id}: unknown access ${m.access}`);
  if (!(m.delivery in catalog.delivery)) fail(`module ${m.id}: unknown delivery ${m.delivery}`);
  for (const field of ["name", "tagline", "summary"]) if (!m[field]) fail(`module ${m.id}: missing ${field}`);
  if (!Array.isArray(m.capabilities) || !m.capabilities.length) fail(`module ${m.id}: no capabilities`);
  if (!m.evidence) fail(`module ${m.id}: no evidence`);
}

const bundled = new Set();
for (const b of catalog.bundles) {
  for (const id of b.modules) {
    if (!modules.has(id)) fail(`bundle ${b.id}: unknown module ${id}`);
    bundled.add(id);
  }
  if (b.price != null && !catalog.pricing.approvedPriceList) {
    fail(`bundle ${b.id} shows a price, but pricing.approvedPriceList names no owner-approved list`);
  }
}
for (const id of modules) if (!bundled.has(id)) fail(`module ${id} is in no bundle`);

// Console coverage, read from the pinned console source.
const modesDir = join(consoleDir, "src/orbital-console/modes");
if (!existsSync(join(modesDir, "registry.ts"))) {
  fail(`console source not found at ${consoleDir}; check out sdn-js/spaceaware-ui or pass --console <dir>`);
} else {
  const registry = readFileSync(join(modesDir, "registry.ts"), "utf8");
  const loaders = registry.match(/MODE_SURFACE_LOADERS[^=]*=[\s\S]*?\}\);/);
  const modeKeys = loaders ? [...loaders[0].matchAll(/^\s*([A-Z]+):/gm)].map((m) => m[1]) : [];
  if (!modeKeys.length) fail("no MODE_SURFACE_LOADERS keys found in registry.ts");

  // Cards: card("hotkey", "glyph", "LABEL", ...) under each mode's key; the
  // sensor-planning cards live on the TRK panel.
  const source = readFileSync(join(modesDir, "modeCards.ts"), "utf8");
  const cardKeys = new Set();
  let mode = null;
  for (const m of source.matchAll(/key:\s*"([A-Z]+)"|SENSOR_PLANNING_CARDS[^=]*=|card\(\s*"[^"]*",\s*"[^"]*",\s*"([^"]+)"/g)) {
    if (m[1]) mode = m[1];
    else if (m[0].startsWith("SENSOR_PLANNING_CARDS")) mode = "TRK";
    else if (mode) cardKeys.add(`${mode}:${m[2]}`);
  }

  const covered = new Set(catalog.modules.flatMap((m) => m.covers || []));
  const excluded = new Map(catalog.notListed.map((n) => [n.key, n.reason]));
  for (const [key, reason] of excluded) if (!reason) fail(`notListed ${key} has no reason`);
  for (const key of modeKeys) {
    if (!covered.has(key) && !excluded.has(key)) {
      fail(`console mode ${key} is neither covered by a module nor in notListed`);
    }
  }
  const consoleKey = (key) => /^[A-Z]+(:|$)/.test(key) && modeKeys.includes(key.split(":")[0]);
  for (const key of [...covered, ...excluded.keys()]) {
    if (!consoleKey(key)) continue;
    if (key.includes(":") ? !cardKeys.has(key) : !modeKeys.includes(key)) {
      fail(`catalog names ${key}, which the console no longer has`);
    }
    if (covered.has(key) && excluded.has(key)) fail(`${key} is both covered and in notListed`);
  }
}

if (errors.length) {
  console.error(`marketing catalog: ${errors.length} problem(s)`);
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}
console.log(`marketing catalog ok: ${catalog.modules.length} modules, ${catalog.bundles.length} bundles`);
