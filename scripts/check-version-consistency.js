#!/usr/bin/env node
/**
 * Verifies that shared dependency and suite versions are consistent across the
 * Space Data Network monorepo.
 */

const fs = require("fs");
const path = require("path");

const REPO_ROOT = path.resolve(__dirname, "..");
const SUITE_MANIFEST_PATH = path.join(REPO_ROOT, "suite.versions.json");

const ROOT_PACKAGE_JSON = "package.json";
const SDN_JS_PACKAGE_JSON = "sdn-js/package.json";
const WEBUI_PACKAGE_JSON = "webui/package.json";
const SDN_SERVER_GO_MOD = "sdn-server/go.mod";
const SDS_SUBMODULE_PACKAGE_JSON = "schemas/sds/package.json";

const OWNED_PACKAGE_JSON_PATHS = [
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

function readJSON(relPath) {
  return JSON.parse(fs.readFileSync(path.join(REPO_ROOT, relPath), "utf8"));
}

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

function getPkgVersion(relPath) {
  const fullPath = path.join(REPO_ROOT, relPath);
  if (!fs.existsSync(fullPath)) return null;
  try {
    const pkg = JSON.parse(fs.readFileSync(fullPath, "utf8"));
    return pkg.version || null;
  } catch {
    return null;
  }
}

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

function stripRange(v) {
  if (!v) return v;
  return v.replace(/^[\^~>=<]+/, "");
}

function normalizeGoVersion(v) {
  return stripRange(v).replace(/\+incompatible$/, "").replace(/^v/, "");
}

function normalizePackageVersion(v) {
  return stripRange(v).replace(/\+.*/, "");
}

const suiteManifest = readJSON("suite.versions.json");

heading("suite manifest consistency");

const rootPackageVersion = getPkgVersion(ROOT_PACKAGE_JSON);
if (rootPackageVersion === suiteManifest.suiteVersion) {
  pass(`root package version matches suite.versions.json: ${rootPackageVersion}`);
} else {
  fail(`root package version mismatch: package.json=${rootPackageVersion} suite.versions.json=${suiteManifest.suiteVersion}`);
}

const webuiVersion = getPkgVersion(WEBUI_PACKAGE_JSON);
if (webuiVersion === suiteManifest.dependencies.ipfsWebUI) {
  pass(`webui version matches suite.versions.json: ${webuiVersion}`);
} else {
  fail(`webui version mismatch: webui/package.json=${webuiVersion} suite.versions.json=${suiteManifest.dependencies.ipfsWebUI}`);
}

heading("spacedatastandards.org version consistency");

const expectedSDS = suiteManifest.dependencies.spacedatastandards;
const jsSDS = stripRange(getPkgDepVersion(SDN_JS_PACKAGE_JSON, "spacedatastandards.org"));
const goSDS = normalizeGoVersion(getGoModVersion(SDN_SERVER_GO_MOD, "github.com/DigitalArsenal/spacedatastandards.org/lib/go"));
const submoduleSDS = normalizePackageVersion(getPkgVersion(SDS_SUBMODULE_PACKAGE_JSON));

if (jsSDS === expectedSDS) {
  pass(`sdn-js spacedatastandards.org matches suite manifest: ${jsSDS}`);
} else {
  fail(`sdn-js spacedatastandards.org mismatch: sdn-js/package.json=${jsSDS} suite.versions.json=${expectedSDS}`);
}

if (goSDS === expectedSDS) {
  pass(`sdn-server Go spacedatastandards.org matches suite manifest: ${goSDS}`);
} else {
  fail(`sdn-server Go spacedatastandards.org mismatch: sdn-server/go.mod=${goSDS} suite.versions.json=${expectedSDS}`);
}

if (submoduleSDS === expectedSDS) {
  pass(`schemas/sds checkout matches suite manifest: ${submoduleSDS}`);
} else {
  fail(`schemas/sds checkout mismatch: schemas/sds/package.json=${submoduleSDS} suite.versions.json=${expectedSDS}`);
}

heading("wallet version consistency");

const expectedHDWalletWasm = suiteManifest.dependencies.hdWalletWasm;
const expectedHDWalletUI = suiteManifest.dependencies.hdWalletUI;
const jsHDWalletWasm = stripRange(getPkgDepVersion(SDN_JS_PACKAGE_JSON, "hd-wallet-wasm"));
const jsHDWalletUI = stripRange(getPkgDepVersion(SDN_JS_PACKAGE_JSON, "hd-wallet-ui"));

if (jsHDWalletWasm === expectedHDWalletWasm) {
  pass(`sdn-js hd-wallet-wasm matches suite manifest: ${jsHDWalletWasm}`);
} else {
  fail(`sdn-js hd-wallet-wasm mismatch: sdn-js/package.json=${jsHDWalletWasm} suite.versions.json=${expectedHDWalletWasm}`);
}

if (jsHDWalletUI === expectedHDWalletUI) {
  pass(`sdn-js hd-wallet-ui matches suite manifest: ${jsHDWalletUI}`);
} else {
  fail(`sdn-js hd-wallet-ui mismatch: sdn-js/package.json=${jsHDWalletUI} suite.versions.json=${expectedHDWalletUI}`);
}

heading("flatbuffers version consistency");

const fbJSVersions = {};
const fbGoVersions = {};
const fbSubmodule = {};

for (const p of OWNED_PACKAGE_JSON_PATHS) {
  const v = getPkgDepVersion(p, "flatbuffers");
  if (v !== null) {
    fbJSVersions[p] = v;
    console.log(`  ${p}: flatbuffers = ${v}`);
  }
}

for (const p of GO_MOD_PATHS) {
  const v = getGoModVersion(p, "github.com/google/flatbuffers");
  if (v !== null) {
    if (p.startsWith("schemas/sds/")) {
      fbSubmodule[p] = v;
    } else {
      fbGoVersions[p] = v;
    }
    console.log(`  ${p}: flatbuffers = ${v}`);
  }
}

const fbNormalizedJS = Object.fromEntries(
  Object.entries(fbJSVersions).map(([file, ver]) => [file, stripRange(ver)])
);
const fbNormalizedGo = Object.fromEntries(
  Object.entries(fbGoVersions).map(([file, ver]) => [file, normalizeGoVersion(ver)])
);
const fbNormalizedSubmodule = Object.fromEntries(
  Object.entries(fbSubmodule).map(([file, ver]) => [file, normalizeGoVersion(ver)])
);

const fbUniqueJS = [...new Set(Object.values(fbNormalizedJS))];
const fbUniqueGo = [...new Set(Object.values(fbNormalizedGo))];
const fbUniqueSubmodule = [...new Set(Object.values(fbNormalizedSubmodule))];

if (fbUniqueJS.length <= 1 && fbUniqueJS.length > 0) {
  pass(`flatbuffers JS version consistent in owned files: ${fbUniqueJS[0]}`);
} else if (fbUniqueJS.length > 1) {
  fail(`flatbuffers JS version mismatch in owned files: ${JSON.stringify(fbNormalizedJS, null, 2)}`);
} else {
  skip("flatbuffers not found in owned JS files");
}

if (fbUniqueGo.length <= 1 && fbUniqueGo.length > 0) {
  pass(`flatbuffers Go version consistent in owned files: ${fbUniqueGo[0]}`);
} else if (fbUniqueGo.length > 1) {
  fail(`flatbuffers Go version mismatch in owned files: ${JSON.stringify(fbNormalizedGo, null, 2)}`);
} else {
  skip("flatbuffers not found in owned Go files");
}

if (fbUniqueJS.length > 0 && fbUniqueGo.length > 0 && fbUniqueJS[0] !== fbUniqueGo[0]) {
  console.log(`  INFO: flatbuffers JS (${fbUniqueJS[0]}) and Go (${fbUniqueGo[0]}) differ across ecosystems`);
}

if (fbUniqueSubmodule.length > 0) {
  console.log(`  INFO: schemas/sds submodule uses flatbuffers ${fbUniqueSubmodule[0]} (external, cannot change)`);
}

heading("flatc-wasm version consistency");

const fcVersions = {};
for (const p of OWNED_PACKAGE_JSON_PATHS) {
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
  skip("flatc-wasm not found in owned files");
}

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
