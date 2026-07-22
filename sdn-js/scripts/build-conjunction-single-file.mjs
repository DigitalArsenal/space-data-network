/**
 * CONJUNCTION-only ship single-file inliner (loop SDN_SPACEAWARE_UI_LOOP.md
 * Phase C, task C1). Sibling of scripts/build-spaceaware-single-file.mjs:
 * takes the Vite output of vite.conjunction.config.mts (dist-conjunction/)
 * and produces ONE self-contained HTML artifact with every script/stylesheet
 * inlined (fonts already data: URIs via assetsInlineLimit), then writes it to
 * sdn-server/cmd/spacedatanetwork/embedded/conjunction_app.html.
 *
 * Per the Phase C banner, this artifact becomes the inline CONTENT of the
 * conjunction APP record (encoding chosen by size at publish time); C2 wires
 * the daemon to serve it. This script does NOT touch any Go serving code — it
 * only writes the artifact file (a sibling of spaceaware_app.html; the daemon's
 * `//go:embed embedded/spaceaware_app.html` is file-specific, so this file is
 * inert until C2 adds its own embed).
 *
 * Beyond the spaceaware inliner's self-containment audit, this script adds a
 * HARD wasm audit: the conjunction ship must carry NO hd-wallet wasm blob (the
 * full app embedded ~5 MB of it as a data URI for the wallet/login flow, which
 * this ship drops). If a wasm signature (`\0asm` raw, or its base64 `AGFzbQ`)
 * appears anywhere in the artifact, the build FAILS.
 *
 * Usage: node scripts/build-conjunction-single-file.mjs
 * (run from sdn-js/, after `vite build --config ui/vite.conjunction.config.mts`)
 */

import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { fileURLToPath } from 'node:url';
import esbuild from 'esbuild';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sdnJsRoot = path.resolve(__dirname, '..');
const distDir = path.join(sdnJsRoot, 'ui', 'dist-conjunction');
const htmlPath = path.join(distDir, 'conjunction.html');
const outPath = path.resolve(
  sdnJsRoot,
  '..',
  'sdn-server',
  'cmd',
  'spacedatanetwork',
  'embedded',
  'conjunction_app.html',
);

function fail(message) {
  console.error(`build-conjunction-single-file: ${message}`);
  process.exit(1);
}

if (!fs.existsSync(htmlPath)) {
  fail(`missing ${htmlPath} — run: npm run build:conjunction:vite`);
}

let html = fs.readFileSync(htmlPath, 'utf8');

function escapeScriptContent(js) {
  return js
    // esbuild emits this whitespace character set with a literal tab before
    // a newline. Escape the same characters so generated HTML stays
    // diff-check clean without changing the JavaScript string value.
    .replaceAll('[...` \t\n\\r\\f', '[...`\\x20\\t\\n\\r\\f')
    .replaceAll('</script', '<\\/script')
    .replaceAll('<!--', '<\\!--');
}

function escapeStyleContent(css) {
  if (css.includes('</style')) {
    fail('CSS bundle contains "</style" — refusing to inline');
  }
  return css;
}

function resolveAssetPath(ref) {
  const clean = ref.replace(/^\.\//, '').replace(/^\//, '');
  const assetPath = path.join(distDir, clean);
  if (!fs.existsSync(assetPath)) {
    fail(`referenced asset not found: ${assetPath}`);
  }
  return assetPath;
}

// ---- Inline module scripts ----
const inlinedScripts = [];
html = html.replace(
  /<script([^>]*?)\ssrc="([^"]+)"([^>]*)><\/script>/g,
  (_match, before, src, after) => {
    const assetPath = resolveAssetPath(src);
    const js = fs.readFileSync(assetPath, 'utf8');
    inlinedScripts.push({ src, bytes: js.length, js });
    const attrs = `${before}${after}`
      .replace(/\scrossorigin(="[^"]*")?/g, '')
      .trim();
    const attrText = attrs.length > 0 ? ` ${attrs}` : '';
    return `<script${attrText}>${escapeScriptContent(js)}</script>`;
  },
);

// ---- Inline stylesheets ----
const inlinedStyles = [];
html = html.replace(
  /<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"[^>]*>/g,
  (_match, href) => {
    const assetPath = resolveAssetPath(href);
    const css = fs.readFileSync(assetPath, 'utf8');
    inlinedStyles.push({ href, bytes: css.length });
    return `<style>${escapeStyleContent(css)}</style>`;
  },
);

// Drop modulepreload hints (everything is inline now).
html = html.replace(/\s*<link[^>]*rel="modulepreload"[^>]*>/g, '');

if (inlinedScripts.length === 0) fail('no <script src> found to inline');
if (inlinedStyles.length === 0) fail('no stylesheet <link> found to inline');

// ---- Self-containment audit: nothing may reference the network or disk ----
const residualRefs = [
  ...html.matchAll(/<script[^>]*\ssrc=/g),
  ...html.matchAll(/<link[^>]*\shref=/g),
  ...html.matchAll(/<img[^>]*\ssrc="(?!data:)/g),
];
if (residualRefs.length > 0) {
  fail(`artifact still references external files: ${residualRefs.map((m) => m[0]).join(', ')}`);
}
const externalUrlPattern = /url\(\s*['"]?(?:https?:)?\/\//g;
if (externalUrlPattern.test(html)) {
  fail('artifact CSS references an external URL');
}
for (const bad of ['fonts.googleapis.com', 'fonts.gstatic.com', 'cdn.jsdelivr.net', 'unpkg.com']) {
  if (html.includes(bad)) {
    fail(`artifact references forbidden host: ${bad}`);
  }
}

// ---- HARD wasm audit: the conjunction ship carries NO hd-wallet wasm blob ----
// The full app embedded ~5 MB of wasm as a data URI for the wallet/login flow;
// this ship keeps no session flow, so a wasm blob here means the hd-wallet stub
// alias (ui/vite.conjunction.config.mts) failed or a session path leaked in.
if (html.includes('\0asm')) {
  fail('artifact embeds a raw wasm module (\\0asm signature present) — hd-wallet wasm leaked in');
}
if (/[;,]base64,[A-Za-z0-9+/]*AGFzbQ/.test(html)) {
  fail('artifact embeds a base64 wasm data URI (AGFzbQ signature present) — hd-wallet wasm leaked in');
}
if (/application\/wasm/.test(html)) {
  fail('artifact references an application/wasm asset — hd-wallet wasm leaked in');
}

// ---- esbuild verification of every inlined script (post-escaping) ----
for (const { src, js } of inlinedScripts) {
  const escaped = escapeScriptContent(js);
  try {
    await esbuild.transform(escaped, { loader: 'js', minify: false });
    await esbuild.transform(js, { loader: 'js', minify: false });
  } catch (error) {
    fail(`esbuild verification failed for inlined ${src}: ${error.message}`);
  }
}

// ---- __SDN_CONFIG__ injection point must exist for the daemon ----
if (!html.includes('</head>')) {
  fail('artifact is missing </head> — daemon __SDN_CONFIG__ injection would fail');
}

fs.mkdirSync(path.dirname(outPath), { recursive: true });
fs.writeFileSync(outPath, html);

const total = Buffer.byteLength(html);
const gzipped = zlib.gzipSync(html, { level: 9 }).length;
console.log('Conjunction single-file artifact written:');
console.log(`  ${outPath}`);
console.log(`  raw   ${total} bytes (${(total / 1024).toFixed(1)} KiB)`);
console.log(`  gzip  ${gzipped} bytes (${(gzipped / 1024).toFixed(1)} KiB)`);
for (const s of inlinedScripts) console.log(`  inlined script ${s.src} (${(s.bytes / 1024).toFixed(1)} KiB)`);
for (const s of inlinedStyles) console.log(`  inlined style ${s.href} (${(s.bytes / 1024).toFixed(1)} KiB)`);
