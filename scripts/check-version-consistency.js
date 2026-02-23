#!/usr/bin/env node
/**
 * check-version-consistency.js
 *
 * Verifies that shared dependency versions are consistent across all
 * package.json and go.mod files in the Space Data Network monorepo.
 *
 * Exit code 0 = all consistent, 1 = mismatch found.
 */

const fs = require("fs");
const path = require("path");

const REPO_ROOT = path.resolve(__dirname, "..");

// Files to check
const PACKAGE_JSON_PATHS = [
  "schemas/sds/package.json",
  "schemas/sds/lib/js/package.json",
  "schemas/sds/packages/standards-explorer/package.json",
  "sdn-js/package.json",
];

const GO_MOD_PATHS = [
  "sdn-server/go.mod",
  "schemas/sds/lib/go/go.mod",
];

let errors = 0;
let checks = 0;

function heading(msg) {
  console.log(`\n--- ${msg} ---`);
}

function pass(msg) {
  checks++;
  console.log(`  PASS: ${msg}`);
}

function fail(msg) {
  checks++;
  errors++;
  console.log(`  FAIL: ${msg}`);
}

function skip(msg) {
  console.log(`  SKIP: ${msg}`);
}

/**
 * Read a package.json and return the dep version for a given package,
 * checking both dependencies and devDependencies.
 * Returns null if file or dep not found.
 */
function getPkgDepVersion(relPath, depName) {
  const fullPath = path.join(REPO_ROOT, relPath);
  if (!fs.existsSync(fullPath)) return null;
  try {
    const pkg = JSON.parse(fs.readFileSync(fullPath, "utf8"));
    const deps = { ...pkg.dependencies, ...pkg.devDependencies };
    return deps[depName] || null;
  } catch {
    return null;
  }
}

/**
 * Read a go.mod and extract the version for a given module path.
 * Returns null if not found.
 */
function getGoModVersion(relPath, modulePath) {
  const fullPath = path.join(REPO_ROOT, relPath);
  if (!fs.existsSync(fullPath)) return null;
  try {
    const content = fs.readFileSync(fullPath, "utf8");
    const re = new RegExp(`${modulePath.replace(/\//g, "\\/")}(?:\\/go)?\\s+(v?\\S+)`);
    const m = content.match(re);
    return m ? m[1] : null;
  } catch {
    return null;
  }
}

/**
 * Strip semver range prefixes (^, ~, >=, etc.) to get the raw version.
 */
function stripRange(v) {
  if (!v) return v;
  return v.replace(/^[\^~>=<]+/, "");
}

// ============================================================================
// Check: flatbuffers version consistency
// ============================================================================
heading("flatbuffers version consistency");

const fbVersions = {};

for (const p of PACKAGE_JSON_PATHS) {
  const v = getPkgDepVersion(p, "flatbuffers");
  if (v !== null) {
    fbVersions[p] = v;
    console.log(`  ${p}: flatbuffers = ${v}`);
  }
}

for (const p of GO_MOD_PATHS) {
  const v = getGoModVersion(p, "github.com/google/flatbuffers");
  if (v !== null) {
    fbVersions[p] = v;
    console.log(`  ${p}: flatbuffers = ${v}`);
  }
}

// Compare: we strip range prefixes and +incompatible suffixes for comparison
// Note: schemas/sds is an external submodule with its own version pins.
// We only enforce consistency among files we control (non-submodule).
const fbNormalized = {};
const fbSubmodule = {};
for (const [file, ver] of Object.entries(fbVersions)) {
  const norm = stripRange(ver).replace(/\+incompatible$/, "").replace(/^v/, "");
  if (file.startsWith("schemas/sds/")) {
    fbSubmodule[file] = norm;
  } else {
    fbNormalized[file] = norm;
  }
}
const fbOwnVersions = [...new Set(Object.values(fbNormalized))];
const fbSubVersions = [...new Set(Object.values(fbSubmodule))];

if (fbOwnVersions.length <= 1 && fbOwnVersions.length > 0) {
  pass(`flatbuffers version consistent in owned files: ${fbOwnVersions[0]}`);
} else if (fbOwnVersions.length > 1) {
  fail(`flatbuffers version mismatch in owned files: ${JSON.stringify(fbNormalized, null, 2)}`);
} else {
  skip("flatbuffers not found in owned files");
}

if (fbSubVersions.length > 0 && fbOwnVersions.length > 0 && fbSubVersions[0] !== fbOwnVersions[0]) {
  console.log(`  INFO: schemas/sds submodule uses flatbuffers ${fbSubVersions[0]} (external, cannot change)`);
}

// ============================================================================
// Check: hd-wallet-wasm version consistency
// ============================================================================
heading("hd-wallet-wasm version consistency");

const hdVersions = {};
for (const p of PACKAGE_JSON_PATHS) {
  const v = getPkgDepVersion(p, "hd-wallet-wasm");
  if (v !== null) {
    hdVersions[p] = v;
    console.log(`  ${p}: hd-wallet-wasm = ${v}`);
  }
}

const hdNormalized = Object.fromEntries(
  Object.entries(hdVersions).map(([f, v]) => [f, stripRange(v)])
);
const hdUnique = [...new Set(Object.values(hdNormalized))];

if (hdUnique.length <= 1 && Object.keys(hdVersions).length > 0) {
  pass(`hd-wallet-wasm version is consistent: ${hdUnique[0]}`);
} else if (hdUnique.length > 1) {
  fail(`hd-wallet-wasm version mismatch: ${JSON.stringify(hdNormalized, null, 2)}`);
} else {
  skip("hd-wallet-wasm not found in any checked files");
}

// ============================================================================
// Check: flatc-wasm version consistency
// ============================================================================
heading("flatc-wasm version consistency");

const fcVersions = {};
for (const p of PACKAGE_JSON_PATHS) {
  const v = getPkgDepVersion(p, "flatc-wasm");
  if (v !== null) {
    fcVersions[p] = v;
    console.log(`  ${p}: flatc-wasm = ${v}`);
  }
}

const fcNormalized = Object.fromEntries(
  Object.entries(fcVersions).map(([f, v]) => [f, stripRange(v)])
);
const fcUnique = [...new Set(Object.values(fcNormalized))];

if (fcUnique.length <= 1 && Object.keys(fcVersions).length > 0) {
  pass(`flatc-wasm version is consistent: ${fcUnique[0]}`);
} else if (fcUnique.length > 1) {
  fail(`flatc-wasm version mismatch: ${JSON.stringify(fcNormalized, null, 2)}`);
} else {
  skip("flatc-wasm not found in any checked files");
}

// ============================================================================
// Summary
// ============================================================================
console.log("\n=======================================");
console.log(`  Checks run: ${checks}`);
console.log(`  Passed:     ${checks - errors}`);
console.log(`  Failed:     ${errors}`);
console.log("=======================================\n");

if (errors > 0) {
  console.log("Version inconsistencies detected! Please align versions across the monorepo.");
  process.exit(1);
} else {
  console.log("All version checks passed.");
  process.exit(0);
}
