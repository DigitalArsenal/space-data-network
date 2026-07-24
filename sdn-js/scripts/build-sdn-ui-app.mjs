import { createHash } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';
import { Builder } from 'flatbuffers';
import { APP, APPT, APPUIPageT, appContentEncoding } from '../node_modules/spacedatastandards.org/lib/js/APP/main.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const dist = path.join(root, 'ui', 'dist-sdnapp');
const index = path.join(dist, 'index.html');
const output = path.join(root, '..', 'sdn-server', 'cmd', 'spacedatanetwork', 'embedded', 'sdn-ui.app');

function fail(message) {
  throw new Error(`build-sdn-ui-app: ${message}`);
}

function asset(ref) {
  const resolved = path.join(dist, ref.replace(/^\.\//, '').replace(/^\//, ''));
  if (!fs.existsSync(resolved)) fail(`missing referenced asset ${resolved}`);
  return resolved;
}

function inlineFlatSqlWorker(document) {
  const assets = path.join(dist, 'assets');
  const workers = fs.readdirSync(assets).filter((name) => /^local-flatsql\.worker-[\w-]+\.js$/.test(name));
  if (workers.length !== 1) fail(`expected one bundled FlatSQL worker, found ${workers.length}`);

  // Vite gives the worker an asset URL even when its module graph has been
  // collapsed. Embed that final worker source in the SDS $APP and turn the
  // generated URL constructor into an in-memory module worker.
  const workerName = workers[0];
  const workerSource = fs.readFileSync(path.join(assets, workerName), 'utf8');
  if (/^\s*import\s/m.test(workerSource)) fail('FlatSQL worker still contains a module import');
  const workerBase64 = Buffer.from(workerSource).toString('base64');
  const workerPattern = new RegExp(
    `new Worker\\(new URL\\(\"\"\\+new URL\\(\"${workerName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\",import\\.meta\\.url\\)\\.href,import\\.meta\\.url\\),\\{type:\"module\"\\}\\)`,
    'g',
  );
  const replacement = `new Worker(URL.createObjectURL(new Blob([atob(\"${workerBase64}\")],{type:\"text/javascript\"})),{type:\"module\"})`;
  const result = document.replace(workerPattern, replacement);
  if (result === document) fail(`could not inline generated FlatSQL worker ${workerName}`);
  return result;
}

let html = fs.readFileSync(index, 'utf8');
html = html.replace(/<script([^>]*?)\ssrc="([^"]+)"([^>]*)><\/script>/g, (_all, before, src, after) => {
  const js = fs.readFileSync(asset(src), 'utf8').replaceAll('</script', '<\\/script').replaceAll('<!--', '<\\!--');
  const attrs = `${before}${after}`.replace(/\scrossorigin(="[^"]*")?/g, '').trim();
  return `<script${attrs ? ` ${attrs}` : ''}>${js}</script>`;
});
html = html.replace(/<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"[^>]*>/g, (_all, href) => {
  const css = fs.readFileSync(asset(href), 'utf8');
  if (css.includes('</style')) fail('stylesheet contains </style');
  return `<style>${css}</style>`;
});
html = html.replace(/\s*<link[^>]*rel="modulepreload"[^>]*>/g, '');
html = inlineFlatSqlWorker(html);
for (const forbidden of [/<script[^>]*\ssrc=/, /<link[^>]*\shref=/, /url\(\s*['"]?(?:https?:)?\/\//]) {
  if (forbidden.test(html)) fail(`non-inline UI reference remains: ${forbidden}`);
}
for (const forbidden of [/coi-serviceworker\.js/, /local-flatsql\.worker-[\w-]+\.js/, /flatsql-[\w-]+\.wasm/]) {
  if (forbidden.test(html)) fail(`runtime asset reference remains: ${forbidden}`);
}
if (!html.includes('</head>')) fail('missing </head> config-injection point');

const content = gzipSync(Buffer.from(html)).toString('base64');
const page = new APPUIPageT(
  'homepage',
  'Space Data Network',
  'The Space Data Network node interface.',
  null,
  null,
  null,
  content,
  appContentEncoding.BASE64_GZIP,
  'text/html',
  createHash('sha256').update(html).digest('hex'),
  true,
);
const app = new APPT('io.spaceaware.sdn-ui', 'Space Data Network', '1.0.0', 'SDN node homepage.', [], [], [], [page]);
const builder = new Builder(1024);
APP.finishSizePrefixedAPPBuffer(builder, app.pack(builder));
fs.mkdirSync(path.dirname(output), { recursive: true });
fs.writeFileSync(output, builder.asUint8Array());
console.log(`wrote ${output} (${html.length} decoded HTML bytes, ${builder.asUint8Array().length} $APP bytes)`);
