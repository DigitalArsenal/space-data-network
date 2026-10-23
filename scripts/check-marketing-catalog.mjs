#!/usr/bin/env node
// Checks docs/marketing/catalog.json, the single source of
// spacedatanetwork.org/marketing.html, against itself and against the pinned
// SpaceAware console:
//   - ids are unique, and every reference (category, access, delivery,
//     bundle member) resolves;
//   - every data source names its provider, data, terms and how it arrives,
//     and powers at least one module that exists;
//   - every module sits in at least one bundle, and the complete bundle holds
//     every module;
//   - no bundle or module shows a price unless an approved price list is named;
//   - access matches the live module catalog policy the evidence quotes:
//     ENTITLED is a paid add-on, and an SDN module that is ANONYMOUS is free;
//   - every console mode is either covered by a module or listed in notListed
//     with a reason, every MODE:CARD key the catalog names still exists, and
//     every console card is in consoleCards (the cards the catalog has been
//     reviewed against).
// A new console mode or card therefore cannot land without a catalog decision.
//
// The console is read from a spaceaware-ui checkout when one is there, else
// straight from the commit sdn pins (git objects), so a task worktree without
// initialised submodules still checks.
//
//   node scripts/check-marketing-catalog.mjs [--console <spaceaware-ui dir>]
import { execFileSync } from "node:child_process";
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

unique(catalog.sources ?? [], "data source");
for (const src of catalog.sources ?? []) {
  for (const field of ["name", "provider", "url", "data", "terms"]) if (!src[field]) fail(`data source ${src.id}: missing ${field}`);
  if (!(src.arrival in (catalog.arrival ?? {}))) fail(`data source ${src.id}: unknown arrival ${src.arrival}`);
  if (!Array.isArray(src.via)) fail(`data source ${src.id}: via must be a list`);
  if (!Array.isArray(src.modules) || !src.modules.length) fail(`data source ${src.id}: powers no module`);
  else for (const id of src.modules) if (!modules.has(id)) fail(`data source ${src.id}: unknown module ${id}`);
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
const complete = catalog.bundles.find((b) => b.id === "complete");
if (complete) for (const id of modules) if (!complete.modules.includes(id)) fail(`bundle complete is missing module ${id}`);
for (const m of catalog.modules) {
  if (m.price != null && !catalog.pricing.approvedPriceList) {
    fail(`module ${m.id} shows a price, but pricing.approvedPriceList names no owner-approved list`);
  }
  const evidence = String(m.evidence || "");
  const entitled = /\bENTITLED\b/.test(evidence), anonymous = /\bANONYMOUS\b/.test(evidence);
  if (entitled && m.access !== "add-on") fail(`module ${m.id}: evidence quotes ENTITLED, so access must be add-on (is ${m.access})`);
  if (m.delivery === "sdn-module") {
    if (!entitled && !anonymous) fail(`module ${m.id}: an SDN module's evidence must quote its live access policy (ANONYMOUS or ENTITLED)`);
    else if (!entitled && m.access !== "free") fail(`module ${m.id}: evidence quotes ANONYMOUS, so access must be free (is ${m.access})`);
  }
}

// The pinned console source: a checkout, or the gitlink's commit.
function consoleReader() {
  if (existsSync(join(consoleDir, "src/orbital-console/modes/registry.ts"))) {
    return (rel) => readFileSync(join(consoleDir, rel), "utf8");
  }
  if (consoleArg >= 0) return null;
  try {
    const git = (...a) => execFileSync("git", ["-C", root, ...a], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
    const sha = git("ls-tree", "HEAD", "sdn-js/spaceaware-ui").split(/\s+/)[2];
    const gitDir = resolve(root, git("rev-parse", "--git-common-dir"), "modules/sdn-js/spaceaware-ui");
    const show = (rel) => execFileSync("git", ["--git-dir", gitDir, "show", `${sha}:${rel}`], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"], maxBuffer: 64 << 20 });
    show("src/orbital-console/modes/registry.ts");
    return show;
  } catch {
    return null;
  }
}

// Console coverage, read from the pinned console source.
const readConsole = consoleReader();
const modesDir = "src/orbital-console/modes";
if (!readConsole) {
  fail(`console source not found at ${consoleDir} or in the pinned sdn-js/spaceaware-ui commit; pass --console <dir>`);
} else {
  const registry = readConsole(`${modesDir}/registry.ts`);
  const loaders = registry.match(/MODE_SURFACE_LOADERS[^=]*=[\s\S]*?\}\);/);
  const modeKeys = loaders ? [...loaders[0].matchAll(/^\s*([A-Z]+):/gm)].map((m) => m[1]) : [];
  if (!modeKeys.length) fail("no MODE_SURFACE_LOADERS keys found in registry.ts");

  // Cards: card("hotkey", "glyph", "LABEL", ...) under each mode's key; the
  // sensor-planning cards live on the TRK panel.
  const source = readConsole(`${modesDir}/modeCards.ts`);
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
  // A mode left out whole (notListed) needs no card-by-card review.
  const reviewed = new Set(catalog.consoleCards || []);
  for (const key of cardKeys) {
    if (excluded.has(key.split(":")[0])) continue;
    if (!reviewed.has(key)) {
      fail(`console card ${key} is new: cover it in a module (covers, capabilities) or add it to notListed, then add it to consoleCards`);
    }
  }
  for (const key of reviewed) if (!cardKeys.has(key)) fail(`consoleCards names ${key}, which the console no longer has`);
}

if (errors.length) {
  console.error(`marketing catalog: ${errors.length} problem(s)`);
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}
console.log(`marketing catalog ok: ${catalog.modules.length} modules, ${catalog.bundles.length} bundles`);
