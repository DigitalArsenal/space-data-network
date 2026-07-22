import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const links = [
  ["standards", "Standards", "https://spacedatastandards.org/"],
  ["flatbuffers", "FlatBuffers", "https://digitalarsenal.github.io/flatbuffers/"],
  ["flatsql", "FlatSQL", "https://digitalarsenal.github.io/flatsql/"],
  ["sdn", "SDN", "https://spacedatanetwork.org/"],
  ["module-sdk", "Module SDK", "https://digitalarsenal.github.io/space-data-module-sdk/"],
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
const approvedRelease = Object.freeze({
  publicStyleUrl: "https://static.spacedatanetwork.org/assets/hd-wallet-ui/2.0.28/sdn-wallet-public-client.c3f68d1cfd88478f10d836a5e829d1dfc6a10157972cf0f7d4d319d0636f2cc4.css",
  publicStyleIntegrity: "sha384-fICuhN4I9xqOK1F5vGGzl26opuO+xQIvTZXMSV76lWTBiQ6AfzztMGLLDw8yRT8i",
  publicScriptUrl: "https://static.spacedatanetwork.org/assets/hd-wallet-ui/2.0.28/sdn-wallet-public-client.f611e7e151a3b5c38384272f8894a4c6634f0a29bf925b0e02e0e27f8db0bfff.js",
  publicScriptIntegrity: "sha384-R+QqsKoWJIS7iWrtPmgJ49DVnb1hSGjZokupbLInJd1PLp8RwQhtF/hnaerc33ci",
  navScriptUrl: "https://static.spacedatanetwork.org/assets/sdn-stack-nav/1.0.0/sdn-stack-nav.52fde607eee38ffa116188201f50258ed50bd18c9f06c1af9678f763147a8fe5.js",
  navScriptIntegrity: "sha384-dgeojhJ8vTszHXIbv7O7nZcEzqD10oUYYJjBmPrV7+kLQEUlXLQD4ek5Q7HOFiO5",
  navStyleUrl: "https://static.spacedatanetwork.org/assets/sdn-stack-nav/1.0.0/sdn-stack-nav.36a36359ce18322185e9ff179f88175bab67d5ad84a14d9c08a54f2ff27267e7.css",
  navStyleIntegrity: "sha384-c4M8Fg+kYaeOYYtJVr7jJsde24IhSWkWaRyAOHEZU9jozhRck089aw+mnHwxymds",
  registrySha256: "e1ce6fe903c9700484a8a87d96581c8cad97063dabf63030b4518a31a3bdaa93",
  callbackHelperUrl: "https://static.spacedatanetwork.org/assets/hd-wallet-ui/2.0.28/sdn-wallet-callback.0f2dee485c8f0d7afe5f70b4b42093a382c92d32af551bd71b41733336091c7a.js",
  callbackHelperIntegrity: "sha384-UZG2zFMk46nuXFtMDyh5Rgfsai9CJXgmqvWILzXVbKJfdbSvg+7GRavCRgLXL5xy",
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

function renderApprovedRegion(clientId, callbackUri) {
  return [
    START_MARKER,
    `<link rel="stylesheet" href="${approvedRelease.publicStyleUrl}" integrity="${approvedRelease.publicStyleIntegrity}" crossorigin="anonymous">`,
    `<script defer src="${approvedRelease.publicScriptUrl}" integrity="${approvedRelease.publicScriptIntegrity}" crossorigin="anonymous" data-sdn-wallet-public-client="v1"></script>`,
    `<script defer src="${approvedRelease.navScriptUrl}" integrity="${approvedRelease.navScriptIntegrity}" crossorigin="anonymous" data-nav-style-url="${approvedRelease.navStyleUrl}" data-nav-style-integrity="${approvedRelease.navStyleIntegrity}" data-wallet-client-url="${approvedRelease.publicScriptUrl}" data-wallet-client-integrity="${approvedRelease.publicScriptIntegrity}" data-wallet-style-url="${approvedRelease.publicStyleUrl}" data-wallet-style-integrity="${approvedRelease.publicStyleIntegrity}" data-wallet-client-id="${clientId}" data-wallet-callback-uri="${callbackUri}" data-wallet-registry-sha256="${approvedRelease.registrySha256}"></script>`,
    END_MARKER,
  ].join("\n");
}

function renderApprovedCallback() {
  return `<!doctype html>\n<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src https://static.spacedatanetwork.org; style-src 'none'; connect-src 'none'; img-src 'none'; font-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><title>Completing wallet connection</title></head><body><p>Completing wallet connection</p><script src="${approvedRelease.callbackHelperUrl}" integrity="${approvedRelease.callbackHelperIntegrity}" crossorigin="anonymous" data-callback-identity="sdn.wallet.callback.v1"></script></body></html>\n`;
}

function assertApprovedRegion(content, path, clientId, callbackUri) {
  if (content.split(START_MARKER).length !== 2 || content.split(END_MARKER).length !== 2) {
    throw new Error(`${path} requires one immutable consumer asset marker pair`);
  }
  const start = content.indexOf(START_MARKER);
  const end = content.indexOf(END_MARKER, start);
  const actual = content.slice(start, end + END_MARKER.length);
  if (actual !== renderApprovedRegion(clientId, callbackUri)) {
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

if (assetPath) {
  const asset = read(assetPath);
  assertContains(asset, "SDN Stack", assetPath);
  assertContains(asset, "customElements.define(\"sdn-stack-nav\"", assetPath);
  assertContains(asset, "--sdn-stack-footer-height", assetPath);
  assertContains(asset, "bottom: 0", assetPath);
  if (asset.includes("top: 0")) {
    throw new Error(`${assetPath} must render as a fixed footer, not a top bar`);
  }
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
}

const landingPages = contents.filter(([path]) => path.endsWith(".html"));
for (const [path, html] of landingPages) {
  assertApprovedRegion(
    html,
    path,
    "sdn-landing-web-v1",
    "https://spacedatanetwork.org/wallet-callback.html",
  );
  if (html.includes("https://spacedatastandards.org/sdn-stack-nav.js")) {
    throw new Error(`${path} contains the mutable SDN Stack nav URL`);
  }
  if (html.includes("consumer-assets.v1.json")) {
    throw new Error(`${path} must not fetch a runtime consumer manifest`);
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
