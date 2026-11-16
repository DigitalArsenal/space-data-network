import { createRequire } from 'node:module';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import os from 'node:os';
import assert from 'node:assert/strict';
// Run against the built node's embedded dashboard and engine.
// Usage: node scripts/dashboard-engine.e2e.mjs <node-binary> [results-directory]
const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(path.resolve(scriptDir, '../package.json'));
if (!process.argv[2])
    throw Error('Pass the built spacedatanetwork executable as the first argument');
const binary = path.resolve(process.argv[2]);
assert.ok(fs.existsSync(binary), 'The supplied node executable must exist');
const folder = process.argv[3] ? path.resolve(process.argv[3]) : fs.mkdtempSync(path.join(os.tmpdir(), 'dashboard-engine-results-'));
fs.mkdirSync(folder, { recursive: true });
const { chromium } = require('@playwright/test');
const { Builder } = require('flatbuffers');
const { CAT } = await import(require.resolve('spacedatastandards.org/lib/js/CAT/CAT.js'));
const label = 'embedded';
const freePort = () => new Promise((resolve, reject) => {
    const server = net.createServer();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
        const port = server.address().port;
        server.close(() => resolve(port));
    });
});
const home = fs.mkdtempSync(path.join(folder, 'node-'));
const port = await freePort(), ingestPort = await freePort();
const config = path.join(home, 'config.yaml');
fs.writeFileSync(config, `mode: full
network:
  listen: [/ip4/127.0.0.1/tcp/0]
  bootstrap: []
  edge_relays: []
  enable_relay: false
storage:
  path: "${home}/data"
  max_size: 1GB
peers:
  strict_mode: false
  enable_dht: false
  enable_mdns: false
admin:
  enabled: true
  listen_addr: "127.0.0.1:${port}"
  require_auth: false
  local_console: true
  tls_mode: disabled
  ipfs_api_url: "http://127.0.0.1:${await freePort()}"
  ipfs_gateway_url: "http://127.0.0.1:${await freePort()}"
gateway:
  anonymous:
    allow: ["/api/v1/data/query", "/api/v1/data/scan"]
publishing:
  enabled: true
  local_publish_addr: "127.0.0.1:${ingestPort}"
setup:
  data_path: "${home}/data"
`);
// Reuse only compiled engine artifacts; node identity and records are isolated.
const cacheRelative = process.platform === 'darwin' ? 'Library/Caches' : '.cache';
const existingCache = path.join(process.env.XDG_CACHE_HOME || path.join(os.homedir(), cacheRelative), 'flatsql-aot');
if (fs.existsSync(existingCache)) {
    fs.mkdirSync(path.join(home, cacheRelative), { recursive: true });
    fs.symlinkSync(existingCache, path.join(home, cacheRelative, 'flatsql-aot'));
}
const out = fs.openSync(path.join(folder, `dashboard-${label}-daemon.log`), 'w');
const child = spawn(binary, ['daemon', '--allow-multi-daemon', '--config', config], { cwd: home, env: { ...process.env, HOME: home, XDG_CACHE_HOME: path.join(home, '.cache'), SDN_STORE_FORMAT: '5', SDM_MODULE_UNSIGNED_BUILD: '1', SDN_MODULE_SIGNING_AUDIT_LOG: path.join(home, 'module-signing.jsonl') }, stdio: ['ignore', out, out] });
let exited = false;
const done = new Promise(r => child.once('exit', (code, signal) => { exited = true; r({ code, signal }); }));
const base = `http://127.0.0.1:${port}`;
const result = { label, consoleErrors: [], pageErrors: [], engineRequests: [], runtimeRequests: [], failedRequests: [], httpErrors: [], rows: 0 };
let browser;
try {
    let up = false;
    const start = Date.now();
    while (!up && !exited && Date.now() - start < 300000) {
        try {
            up = (await fetch(base + '/api/v1/health', { signal: AbortSignal.timeout(2000) })).ok;
        }
        catch { }
        if (!up)
            await new Promise(r => setTimeout(r, 500));
    }
    if (!up)
        throw Error('node health timeout');
    for (let i = 0; i < 3; i++) {
        const b = new Builder(256), name = b.createString(`Synthetic dashboard record ${i}`);
        CAT.startCAT(b);
        CAT.addObjectName(b, name);
        CAT.addNoradCatId(b, 900001 + i);
        CAT.finishSizePrefixedCATBuffer(b, CAT.endCAT(b));
        const resp = await fetch(`http://127.0.0.1:${ingestPort}/api/v1/data/publish/CAT.fbs?provider_id=synthetic&source_name=synthetic`, { method: 'POST', headers: { 'content-type': 'application/vnd.sdn.flatbuffers.stream' }, body: b.asUint8Array() });
        if (!resp.ok)
            throw Error(`ingest ${resp.status}`);
    }
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
    page.setDefaultTimeout(15000);
    page.on('console', message => {
        if (message.type() === 'error') result.consoleErrors.push(message.text());
    });
    page.on('pageerror', e => result.pageErrors.push(String(e)));
    page.on('requestfailed', r => result.failedRequests.push({ url: r.url(), error: r.failure() }));
    page.on('response', response => {
        const entry = { url: response.url(), status: response.status() };
        if (entry.status >= 400) result.httpErrors.push(entry);
        if (entry.url.includes('/sdn-js/runtime/')) result.runtimeRequests.push(entry);
        if (entry.url.includes('flatsql.wasm')) result.engineRequests.push(entry);
    });
    await page.route('**/*', route => {
        const url = new URL(route.request().url());
        return ['127.0.0.1', 'localhost'].includes(url.hostname) ? route.continue() : route.abort();
    });
    const response = await page.goto(base + '/#/data/cat', { waitUntil: 'domcontentloaded' });
    result.csp = response.headers()['content-security-policy'];
    assert.ok(result.csp && !result.csp.includes("'unsafe-eval'"), 'dashboard must retain its strict JavaScript CSP');
    assert.equal(await page.evaluate(() => globalThis.crossOriginIsolated), true);
    await page.getByText(/^Synthetic dashboard record [0-2]$/).nth(2).waitFor({ timeout: 60000 }).catch(() => { });
    fs.writeFileSync(path.join(folder, `dashboard-${label}-body.txt`), await page.locator('body').innerText());
    result.rows = await page.getByText(/^Synthetic dashboard record [0-2]$/).count();
    result.url = page.url();
    await page.screenshot({ path: path.join(folder, `dashboard-${label}.png`), timeout: 5000 }).catch(() => { });
    assert.ok(result.engineRequests.some(r => r.status === 200), 'dashboard must request its embedded engine successfully');
    assert.equal(result.rows, 3, 'dashboard must query and render all three ingested records');
    for (let i = 0; i < 3; i++)
        assert.equal(await page.getByText(`Synthetic dashboard record ${i}`, { exact: true }).count(), 1);
    assert.deepEqual(result.pageErrors, [], 'dashboard must not have uncaught JavaScript errors');
}
catch (e) {
    result.error = String(e);
    process.exitCode = 1;
}
finally {
    if (browser) await browser.close().catch(error => {
        result.closeError = String(error);
        process.exitCode = 1;
    });
    if (!exited) {
        console.log('Stopping owned daemon', child.pid, child.spawnargs.join(' '));
        child.kill('SIGTERM');
        await Promise.race([done, new Promise(r => setTimeout(r, 15000))]);
        if (!exited)
            child.kill('SIGKILL');
    }
    await done;
    fs.writeFileSync(path.join(folder, `dashboard-${label}.json`), JSON.stringify(result, null, 2));
    fs.rmSync(home, { recursive: true, force: true });
    console.log(JSON.stringify(result, null, 2));
    if (!process.argv[3])
        fs.rmSync(folder, { recursive: true, force: true });
}
