#!/usr/bin/env node
// One browser flow for the marketing overview, inventory, plan assignments and references.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const { stampMarketingAssets } = await import('./marketing-assets-stamp.mjs');
assert.deepEqual(stampMarketingAssets(), [], 'Marketing asset URLs carry the current content stamp (node scripts/marketing-assets-stamp.mjs)');
const arg = (name) => { const i = process.argv.indexOf(name); return i < 0 ? null : process.argv[i + 1]; };
let stack = arg('--stack-root') || process.env.GAUNTLET_GATE_ROOT || process.env.GAUNTLET_STACK_ROOT;
if (!stack) {
  let dir = path.resolve(root, execFileSync('git', ['rev-parse', '--git-common-dir'], { cwd: root, encoding: 'utf8' }).trim());
  while (dir !== path.dirname(dir)) {
    if (fs.existsSync(path.join(dir, 'graph/e2e/headless.mjs'))) { stack = dir; break; }
    dir = path.dirname(dir);
  }
}
assert(stack, 'Pass --stack-root pointing to the stack browser harness');
if (arg('--stack-root') || process.env.GAUNTLET_GATE_ROOT) {
  const registry = JSON.parse(fs.readFileSync(path.join(stack, 'graph/gauntlet/visual-impact.json'), 'utf8'));
  assert(registry.features['marketing-plans'], 'Marketing overview is registered');
  assert(registry.features['marketing-inventory'].dependsOn.includes('marketing-plans'), 'Inventory tracks plan changes');
  for (const page of ['docs/marketing.html', 'docs/marketing-modules.html']) {
    assert(registry.routes.some((r) => r.component === 'sdn' && r.paths.includes(page)), `Visual coverage routes ${page}`);
  }
}
const { withHeadlessShell, openTab } = await import(pathToFileURL(path.join(stack, 'graph/e2e/headless.mjs')));
const out = arg('--out') || fs.mkdtempSync(path.join(os.tmpdir(), 'marketing-plans-e2e-'));
fs.mkdirSync(out, { recursive: true });
const site = path.join(root, 'docs');
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' };
const server = http.createServer((req, res) => {
  const file = path.resolve(site, '.' + decodeURIComponent(new URL(req.url, 'http://localhost').pathname));
  if (!file.startsWith(site + path.sep) || !fs.existsSync(file) || !fs.statSync(file).isFile()) { res.writeHead(404); res.end(); return; }
  res.writeHead(200, { 'content-type': mime[path.extname(file)] || 'application/octet-stream' });
  if (req.method === 'HEAD') res.end(); else fs.createReadStream(file).pipe(res);
});
const liveURL = arg('--url');
if (!liveURL) await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const origin = liveURL ? new URL(liveURL).origin : `http://127.0.0.1:${server.address().port}`;
const errors = [], failedAssets = [], requests = new Map();
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
try {
  await withHeadlessShell(async (browser) => {
    const { cdp, close } = await openTab(browser);
    try {
      cdp.on((method, p) => {
        if (method === 'Runtime.exceptionThrown') errors.push(p.exceptionDetails.exception?.description || p.exceptionDetails.text);
        if (method === 'Runtime.consoleAPICalled' && p.type === 'error') errors.push(p.args.map((a) => a.value || a.description).join(' '));
        if (method === 'Network.requestWillBeSent') requests.set(p.requestId, p.request.url);
        if (method === 'Network.responseReceived' && p.response.url.startsWith(origin) && p.response.status >= 400) failedAssets.push(`${p.response.status} ${p.response.url}`);
        if (method === 'Network.loadingFailed' && !p.canceled && requests.get(p.requestId)?.startsWith(origin)) failedAssets.push(requests.get(p.requestId));
      });
      for (const domain of ['Page', 'Runtime', 'Network']) await cdp.send(`${domain}.enable`);
      await cdp.send('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: out });
      const evaluate = async (expression) => {
        const r = await cdp.send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
        assert(!r.exceptionDetails, r.exceptionDetails?.exception?.description || 'Browser evaluation failed');
        return r.result.value;
      };
      const waitFor = async (expression) => {
        const end = Date.now() + 15000;
        while (Date.now() < end) { if (await evaluate(expression)) return; await sleep(100); }
        assert.fail(`Timed out: ${expression}`);
      };
      const click = async (selector) => {
        const pos = await evaluate(`(() => { const e = document.querySelector(${JSON.stringify(selector)}); if (!e) return null; e.scrollIntoView({block:'center',behavior:'instant'}); const r=e.getClientRects()[0]; return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`);
        assert(pos, `Missing ${selector}`);
        await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...pos });
        await cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...pos });
      };
      const select = (selector, value) => evaluate(`(() => { const e=document.querySelector(${JSON.stringify(selector)}); e.value=${JSON.stringify(value)}; e.dispatchEvent(new Event('input',{bubbles:true})); e.dispatchEvent(new Event('change',{bubbles:true})); })()`);
      const fill = async (selector, text) => { await click(selector); await evaluate(`document.querySelector(${JSON.stringify(selector)}).select()`); await cdp.send('Input.insertText', { text }); };
      const screenshot = async (name) => {
        const { cssContentSize: size } = await cdp.send('Page.getLayoutMetrics');
        const full = name.startsWith('overview');
        // Full-page captures stop at the headless renderer's 8192 px surface limit (the phone overview runs longer).
        const shot = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: full, ...(full ? { clip: { x: 0, y: 0, width: size.width, height: Math.min(size.height, 8192), scale: 1 } } : {}) });
        fs.writeFileSync(path.join(out, name + '.png'), Buffer.from(shot.data, 'base64'));
      };
      const navigate = async (url, ready) => { await cdp.send('Page.navigate', { url: origin + url }); await waitFor(ready); };
      const noOverflow = async () => assert(await evaluate('document.documentElement.scrollWidth <= innerWidth + 1'), 'Page overflows horizontally');
      const localLinks = async () => {
        const links = await evaluate(`Array.from(document.querySelectorAll('a[href]'), a=>a.href).filter(x=>x.startsWith(${JSON.stringify(origin)}))`);
        for (const url of new Set(links.map((x) => x.split('#')[0]))) assert.equal((await fetch(url, { method: 'HEAD' })).status, 200, `Broken link ${url}`);
      };
      await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
      await navigate('/marketing.html', 'document.querySelectorAll(".mp-plan").length === 4');
      console.log('Marketing overview loaded');
      assert.deepEqual(await evaluate('Array.from(document.querySelectorAll(".mp-plan"), e=>[e.dataset.plan,e.querySelector(".mp-price strong").textContent])'), [['free', '$0'], ['analysis', '$5'], ['ai', '$40'], ['spaceedge', 'Contact us']]);
      assert.equal(await evaluate('document.querySelectorAll("[data-inv]").length'), 0, 'Overview must not contain inventory controls');
      assert.match(await evaluate('document.querySelector("[data-plans=data-rule]").textContent'), /without a subscription or sign-in/);
      assert.deepEqual(await evaluate('Array.from(document.querySelectorAll(".mp-rights-group"), e=>e.dataset.rights)'), ['general', 'non-commercial', 'results'], 'Three rights groups');
      const inGroup = (group, id) => evaluate(`!!document.querySelector('[data-rights="${group}"] [data-source="${id}"]')`);
      assert(await inGroup('general', 'celestrak-gp') && await inGroup('general', 'osm-tiles'), 'General-purpose sources listed');
      assert(await inGroup('non-commercial', 'open-meteo'), 'Non-commercial source listed');
      assert(await inGroup('results', 'space-track') && await inGroup('results', 'starlink-ephemeris'), 'Results-only sources listed');
      assert.equal(await evaluate('document.querySelectorAll(".mp-rights-group [data-source]").length'), await evaluate('(async () => (await (await fetch("/marketing/catalog.json")).json()).sources.length)()'), 'Every catalog source sits in one rights group');
      await click('a[href="#plans"]');
      assert.equal(await evaluate('location.hash'), '#plans');
      await evaluate('scrollTo(0,0)');
      await noOverflow(); await localLinks(); await screenshot('overview-desktop');
      await click('[data-sdn-theme-toggle]');
      await screenshot('overview-alternate-theme');
      await click('[data-sdn-theme-toggle]');
      await cdp.send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
      await evaluate('scrollTo(0,0)'); await noOverflow(); await screenshot('overview-mobile');
      await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
      await click('[data-plan="analysis"] a');
      await waitFor('document.querySelector("[data-key=plan]")?.value === "analysis"');
      console.log('Inventory navigation and plan filter loaded');
      assert(await evaluate('!!document.querySelector(".inv-row[data-id=sensor-coverage]")'));
      await fill('[data-key=q]', 'sensor');
      assert(await evaluate('document.querySelectorAll(".inv-row").length > 0 && document.querySelectorAll(".inv-row").length < 16'), 'Search narrows the Analysis tier');
      await click('.inv-row[data-id="sensor-coverage"]');
      assert.equal(await evaluate('document.querySelector(".inv-row[data-id=sensor-coverage]").getAttribute("aria-expanded")'), 'true');
      await screenshot('inventory-sensors');
      await click('.inv-row[data-id="sensor-coverage"]');
      assert.equal(await evaluate('document.querySelector(".inv-row[data-id=sensor-coverage]").getAttribute("aria-expanded")'), 'false');
      await cdp.send('Page.reload'); await waitFor('document.querySelector("[data-key=q]")?.value === "sensor"');
      assert.equal(await evaluate('document.querySelector("[data-key=plan]").value'), 'analysis');
      await click('[data-act=reset]');
      await select('[data-key=plan]', 'free');
      assert(await evaluate('!!document.querySelector(".inv-row[data-id=star-field]")'));
      await fill('[data-key=q]', 'no-such-module-xyz');
      assert.match(await evaluate('document.querySelector("[data-inv=view]").textContent'), /No modules match/);
      await click('[data-act=reset]');
      await select('[data-key=plan]', 'ai');
      assert.deepEqual(await evaluate('Array.from(document.querySelectorAll(".inv-row"),r=>r.dataset.id)'), ['imagery-detection']);
      await click('[data-act=csv]');
      for (let i = 0; i < 50 && !fs.readdirSync(out).some((f) => f.endsWith('.csv')); i++) await sleep(100);
      const csv = fs.readdirSync(out).find((f) => f.endsWith('.csv'));
      assert(csv, 'CSV downloaded');
      const exported = fs.readFileSync(path.join(out, csv), 'utf8');
      assert.match(exported, /"plan"/); assert.match(exported, /"imagery-detection"/); assert.match(exported, /"AI"/);
      await click('[data-act=reset]');
      for (const view of ['sources', 'matrix', 'bundles', 'cards', 'excluded', 'modules']) {
        await click(`[data-view="${view}"]`);
        assert.equal(await evaluate(`document.querySelector('[data-view="${view}"]').getAttribute('aria-selected')`), 'true');
        assert(await evaluate('document.querySelector("[data-inv=view]").textContent.trim().length > 0'));
        if (view === 'sources') assert.match(await evaluate('document.querySelector(".inv-sources thead").textContent'), /Rights/);
      }
      await click('[data-sort=name]');
      assert.equal(await evaluate('document.querySelector("th[aria-sort=descending]").textContent.trim()'), 'Module ↓');
      await click('[data-act=expand]');
      assert(await evaluate('Array.from(document.querySelectorAll(".inv-row")).every(r=>r.getAttribute("aria-expanded")==="true")'));
      await click('[data-act=expand]');
      assert.equal(await evaluate('document.querySelectorAll(".inv-detail").length'), 0);
      await click('#reference-details > summary');
      await waitFor('document.querySelectorAll("[data-udl=table] tbody tr").length > 10');
      assert.deepEqual(await evaluate('Array.from(document.querySelectorAll("[data-uplan] option")).slice(1).map(e=>e.value)'), ['free', 'analysis', 'ai', 'spaceedge']);
      await fill('[data-uq]', 'Star catalog');
      assert.deepEqual(await evaluate('Array.from(document.querySelectorAll("[data-udl=table] tbody td.ud-t"),e=>e.textContent)'), ['Open', 'Open', 'Open', 'UDL']);
      await click('[data-wording=military]');
      assert.match(await evaluate('document.querySelector("[data-se=warfighting]").textContent'), /Space combat/);
      await click('[data-wording=academic]');
      assert.match(await evaluate('document.querySelector("[data-se=warfighting]").textContent'), /Space combat scenario/);
      await click('[data-wording=both]');
      await click('#reference-details > summary');
      await localLinks();
      console.log('Inventory interactions, CSV and supporting references passed');
      await evaluate('scrollTo(0,0)'); await screenshot('inventory-desktop');
      await cdp.send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
      await noOverflow(); await screenshot('inventory-mobile');
      await click('a[href="marketing.html"]');
      await waitFor('document.querySelectorAll(".mp-plan").length === 4');
      assert.deepEqual(errors, [], 'Browser errors'); assert.deepEqual(failedAssets, [], 'Failed local assets');
    } finally { await close(); }
  });
  fs.writeFileSync(path.join(out, 'result.json'), JSON.stringify({ passed: true, origin, errors, failedAssets }, null, 2));
  console.log(`MARKETING E2E PASS: four tiers, free data, filters, details, CSV, references, navigation and mobile. Screenshots: ${out}`);
} finally { server.close(); }
