/**
 * SpaceAware UI single-file inliner (loop SDN_SPACEAWARE_UI_LOOP.md,
 * packaging hard rule 2026-07-06): the UI ships INSIDE the sdn-server binary
 * as inline strings, never as separate files.
 *
 * Takes the Vite output of vite.spaceaware.config.mts (dist-spaceaware/) and
 * produces ONE self-contained HTML artifact with every script and stylesheet
 * inlined (fonts/images are already data: URIs via assetsInlineLimit), then
 * writes it to sdn-server/cmd/spacedatanetwork/embedded/spaceaware_app.html
 * where it is go:embed-ded and served from memory.
 *
 * Prior art: OrbPro scripts/build-single-file.js — carried lessons:
 * - never splice content into the middle of a bundle except at safe
 *   boundaries (here: whole-file inlining only, with `</script`-safe
 *   escaping instead of mid-bundle marker injection), and
 * - ALWAYS re-verify the inlined JS with esbuild afterwards so a corrupted
 *   multi-KB/MB inline is caught at build time, not in the browser.
 *
 * Usage: node scripts/build-spaceaware-single-file.mjs
 * (run from sdn-js/, after `vite build --config ui/vite.spaceaware.config.mts`)
 */

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import esbuild from 'esbuild';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sdnJsRoot = path.resolve(__dirname, '..');
const distDir = path.join(sdnJsRoot, 'ui', 'dist-spaceaware');
const htmlPath = path.join(distDir, 'spaceaware.html');
const outPath = path.resolve(
  sdnJsRoot,
  '..',
  'sdn-server',
  'cmd',
  'spacedatanetwork',
  'embedded',
  'spaceaware_app.html',
);

function fail(message) {
  console.error(`build-spaceaware-single-file: ${message}`);
  process.exit(1);
}

if (!fs.existsSync(htmlPath)) {
  fail(`missing ${htmlPath} — run: npm run build:spaceaware:vite`);
}

let html = fs.readFileSync(htmlPath, 'utf8');

/**
 * Escape a JS bundle for inlining into a <script> element. `<\/` is
 * byte-identical to `</` inside JS string and regex literals, so this is a
 * semantics-preserving rewrite for those (the esbuild verification below
 * catches any case where it is not).
 */
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

// ---- esbuild verification of every inlined script (post-escaping) ----
for (const { src, js } of inlinedScripts) {
  const escaped = escapeScriptContent(js);
  // What the browser executes is the escaped text as parsed out of the HTML
  // — for JS parsing purposes the escaped text itself must remain valid.
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
console.log('SpaceAware single-file artifact written:');
console.log(`  ${outPath}`);
console.log(`  total ${(total / 1024).toFixed(1)} KiB`);
for (const s of inlinedScripts) console.log(`  inlined script ${s.src} (${(s.bytes / 1024).toFixed(1)} KiB)`);
for (const s of inlinedStyles) console.log(`  inlined style ${s.href} (${(s.bytes / 1024).toFixed(1)} KiB)`);
