import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";

const links = [
  ["standards", "Standards", "https://spacedatastandards.org/"],
  ["flatbuffers", "FlatBuffers", "https://digitalarsenal.github.io/flatbuffers/"],
  ["flatsql", "FlatSQL", "https://digitalarsenal.github.io/flatsql/"],
  ["sdn", "SDN", "https://spacedatanetwork.org/"],
  ["module-sdk", "Module SDK", "https://digitalarsenal.github.io/space-data-module-sdk/"],
  ["hd-wallet", "HD Wallet", "https://digitalarsenal.github.io/hd-wallet-wasm/"],
];

const headerTokens = [
  ["--sdn-stack-header-height", "52px"],
  ["--sdn-stack-header-brand-size", "15px"],
  ["--sdn-stack-header-link-size", "14px"],
  ["--sdn-stack-header-action-size", "13px"],
  ["--sdn-stack-header-mobile-link-size", "16px"],
];

const START_MARKER = "<!-- SDN_CONSUMER_ASSETS_START -->";
const END_MARKER = "<!-- SDN_CONSUMER_ASSETS_END -->";
const NAV_RELEASE_DIR = "docs/assets/vendor/sdn-stack-nav";
const WALLET_DIR = "docs/assets/sdn-wallet";

// The reviewed release of what every page that shows the strip loads. The nav
// is the sdn-stack-nav release of record (spacedatastandards.org
// dist/static-assets), vendored under this site's own root, so the site
// requests no other origin for it. The wallet client files are this site's own
// copies, pinned by digest.
const approvedRelease = Object.freeze({
  navVersion: "1.2.0",
  navScriptUrl: "/assets/vendor/sdn-stack-nav/1.2.0/sdn-stack-nav.ba268c55a1551bcacc390ac01347c319335a1660787b55b4b239df00fc3c1a4c.js",
  navScriptIntegrity: "sha384-4BDI84CnpZ83otmTHK/KzA5+4pw8q5l+3LInURbEVJY96/iLGzCZUZsCyxjZ1Kpb",
  navStyleUrl: "/assets/vendor/sdn-stack-nav/1.2.0/sdn-stack-nav.36a36359ce18322185e9ff179f88175bab67d5ad84a14d9c08a54f2ff27267e7.css",
  navStyleIntegrity: "sha384-c4M8Fg+kYaeOYYtJVr7jJsde24IhSWkWaRyAOHEZU9jozhRck089aw+mnHwxymds",
  walletFiles: Object.freeze({
    [`${WALLET_DIR}/sdn-wallet-public-client.css`]: "c3f68d1cfd88478f10d836a5e829d1dfc6a10157972cf0f7d4d319d0636f2cc4",
    [`${WALLET_DIR}/sdn-wallet-public-client.js`]: "f611e7e151a3b5c38384272f8894a4c6634f0a29bf925b0e02e0e27f8db0bfff",
    [`${WALLET_DIR}/sdn-wallet-callback.js`]: "0f2dee485c8f0d7afe5f70b4b42093a382c92d32af551bd71b41733336091c7a",
  }),
});

const args = process.argv.slice(2);
const assetIndex = args.indexOf("--asset");
const assetPath = assetIndex === -1 ? null : args[assetIndex + 1];
const filteredArgs = assetIndex === -1 ? args : args.slice(0, assetIndex);
const [active, ...contentPaths] = filteredArgs;

if (!active || contentPaths.length === 0) {
  throw new Error("Usage: node scripts/check-sdn-stack-nav.mjs <active> <content...> [--asset <path>]");
}

function read(path) {
  return readFileSync(path, "utf8");
}

function assertContains(content, expected, path) {
  if (!content.includes(expected)) {
    throw new Error(`${path} is missing ${expected}`);
  }
}

// The wallet client files sit in docs/assets/sdn-wallet; a page reaches them by its own relative path.
function walletRootFor(page) {
  const up = relative(dirname(resolve(page)), resolve(WALLET_DIR)).split("\\").join("/");
  return `${up}/`;
}

function renderApprovedRegion(walletRoot) {
  return [
    START_MARKER,
    `<link rel="stylesheet" href="${walletRoot}sdn-wallet-public-client.css">`,
    `<script defer src="${walletRoot}sdn-wallet-public-client.js"></script>`,
    `<script defer src="${approvedRelease.navScriptUrl}" integrity="${approvedRelease.navScriptIntegrity}" crossorigin="anonymous" data-nav-style-url="${approvedRelease.navStyleUrl}" data-nav-style-integrity="${approvedRelease.navStyleIntegrity}"></script>`,
    END_MARKER,
  ].join("\n");
}

function renderApprovedCallback() {
  return `<!doctype html>\n<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'self'; style-src 'none'; connect-src 'none'; img-src 'none'; font-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><title>Completing wallet connection</title></head><body><p>Completing wallet connection</p><script src="assets/sdn-wallet/sdn-wallet-callback.js" data-callback-identity="sdn.wallet.callback.v1"></script></body></html>\n`;
}

function assertApprovedRegion(content, path) {
  if (content.split(START_MARKER).length !== 2 || content.split(END_MARKER).length !== 2) {
    throw new Error(`${path} requires one immutable consumer asset marker pair`);
  }
  const start = content.indexOf(START_MARKER);
  const end = content.indexOf(END_MARKER, start);
  const actual = content.slice(start, end + END_MARKER.length);
  if (actual !== renderApprovedRegion(walletRootFor(path))) {
    throw new Error(`${path} consumer assets differ from the reviewed release`);
  }
}

function checkContent(path) {
  const content = read(path);
  if (content.includes("--sdn-stack-nav-height")) {
    throw new Error(`${path} must not reserve top header space for the SDN Stack nav`);
  }
  if (content.includes("Space Stack")) {
    throw new Error(`${path} must use SDN Stack, not Space Stack`);
  }
  return content;
}

// Every page of the site, whatever its depth.
function htmlPages(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return entry.name === "assets" ? [] : htmlPages(path);
    return entry.name.endsWith(".html") ? [path] : [];
  });
}

const contents = contentPaths.map((path) => [path, checkContent(path)]);

if (!contents.some(([, content]) => /sdn-stack-nav(?:\.[0-9a-f]{64})?\.js/u.test(content))) {
  throw new Error("Expected a page shell to load an SDN Stack nav script");
}

if (!contents.some(([, content]) => content.includes("<sdn-stack-nav"))) {
  throw new Error("Expected a page shell to render <sdn-stack-nav>");
}

if (!contents.some(([, content]) => content.includes(`active="${active}"`))) {
  throw new Error(`Expected an sdn-stack-nav element with active="${active}"`);
}

if (!contents.some(([, content]) => content.includes('href="#stack"'))) {
  throw new Error("Expected a header Stack link pointing to #stack");
}

if (!contents.some(([, content]) => content.includes('id="stack"'))) {
  throw new Error("Expected a local #stack section");
}

for (const [name, value] of headerTokens) {
  const declaration = `${name}: ${value}`;
  if (!contents.some(([, content]) => content.includes(declaration))) {
    throw new Error(`Expected shared header token ${declaration}`);
  }
}

if (!contents.some(([, content]) => content.includes("height: var(--sdn-stack-header-height, 52px)"))) {
  throw new Error("Expected header height to use --sdn-stack-header-height");
}

if (!contents.some(([, content]) => content.includes("font-size: var(--sdn-stack-header-link-size, 14px)"))) {
  throw new Error("Expected desktop header links to use --sdn-stack-header-link-size");
}

// Every page that renders the strip carries the reviewed release, not only the pages named on the command line.
const stripPages = htmlPages("docs").filter((page) => read(page).includes("<sdn-stack-nav"));
if (stripPages.length === 0) throw new Error("No page of docs/ renders <sdn-stack-nav>");
for (const page of stripPages) {
  const html = read(page);
  assertApprovedRegion(html, page);
  if (!html.includes(`<sdn-stack-nav active="${active}">`)) {
    throw new Error(`${page} must render <sdn-stack-nav active="${active}">`);
  }
  if (html.includes("static.spacedatanetwork.org")) {
    throw new Error(`${page} names static.spacedatanetwork.org`);
  }
  if (html.includes("https://spacedatastandards.org/sdn-stack-nav.js")) {
    throw new Error(`${page} contains the mutable SDN Stack nav URL`);
  }
  if (html.includes("consumer-assets.v1.json")) {
    throw new Error(`${page} must not fetch a runtime consumer manifest`);
  }
}

// The release files: exactly the pinned pair under exactly the pinned version, each the bytes its name and SRI claim.
const releaseDirs = readdirSync(NAV_RELEASE_DIR);
if (releaseDirs.length !== 1 || releaseDirs[0] !== approvedRelease.navVersion) {
  throw new Error(`${NAV_RELEASE_DIR} must hold only ${approvedRelease.navVersion} (found ${releaseDirs.join(", ") || "nothing"})`);
}
if (existsSync("docs/assets/sdn-stack-nav")) {
  throw new Error("docs/assets/sdn-stack-nav is a retired local copy of the nav; the strip loads the vendored release");
}
const pinned = [
  [approvedRelease.navScriptUrl, approvedRelease.navScriptIntegrity],
  [approvedRelease.navStyleUrl, approvedRelease.navStyleIntegrity],
];
const vendored = readdirSync(join(NAV_RELEASE_DIR, approvedRelease.navVersion)).sort();
const pinnedFiles = pinned.map(([url]) => url.slice(url.lastIndexOf("/") + 1)).sort();
if (JSON.stringify(vendored) !== JSON.stringify(pinnedFiles)) {
  throw new Error(`${NAV_RELEASE_DIR}/${approvedRelease.navVersion} holds ${vendored.join(", ")}, expected ${pinnedFiles.join(", ")}`);
}
for (const [url, integrity] of pinned) {
  const file = join("docs", url);
  const bytes = readFileSync(file);
  const digest = url.split(".").at(-2);
  if (createHash("sha256").update(bytes).digest("hex") !== digest) throw new Error(`${file} differs from the digest in its name`);
  if (`sha384-${createHash("sha384").update(bytes).digest("base64")}` !== integrity) throw new Error(`${file} differs from its pinned integrity`);
}
for (const [file, digest] of Object.entries(approvedRelease.walletFiles)) {
  if (createHash("sha256").update(readFileSync(file)).digest("hex") !== digest) {
    throw new Error(`${file} differs from the reviewed wallet client`);
  }
}

if (assetPath) {
  const asset = read(assetPath);
  assertContains(asset, "SDN Stack", assetPath);
  assertContains(asset, "customElements.define(\"sdn-stack-nav\"", assetPath);
  assertContains(asset, "--sdn-stack-footer-height", assetPath);
  if (asset.includes("--sdn-stack-nav-height")) {
    throw new Error(`${assetPath} must not publish a top nav offset variable`);
  }
  for (const [, label, href] of links) {
    assertContains(asset, label, assetPath);
    assertContains(asset, href, assetPath);
  }
  if (asset.includes("Space Stack")) {
    throw new Error(`${assetPath} must use SDN Stack, not Space Stack`);
  }
  // The strip is a fixed footer bar, styled by the released stylesheet.
  const style = read(join("docs", approvedRelease.navStyleUrl));
  if (!/--sdn-stack-footer-height,\s*40px/u.test(style) || !/bottom:\s*0/u.test(style) || /top:\s*0/u.test(style)) {
    throw new Error("the released stylesheet must render the strip as a fixed footer, not a top bar");
  }
}

const callbackPath = resolve("docs/wallet-callback.html");
let callback = "";
try {
  callback = read(callbackPath);
} catch {
  throw new Error("Missing dedicated SDN wallet callback");
}
if (callback !== renderApprovedCallback()) {
  throw new Error(`${callbackPath} differs from the reviewed callback release`);
}

process.stdout.write(`sdn-stack-nav ${approvedRelease.navVersion}: ${stripPages.length} pages carry the reviewed release\n`);
