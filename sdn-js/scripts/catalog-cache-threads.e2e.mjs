// End to end: the browser catalog cache opens on the built runtime in fresh
// browser contexts with the processor slowed, the way the SpaceAware console
// opens it, and no context ends in a FlatSQL engine fault.
//
// The console imports the built package (`dist/index.mjs`) and opens
// `FlatSQLEngineRecordStore.open({ persistenceKey: "spaceaware-catalog",
// schemas })` (OPFS, the runtime's own guest, thread and I/O workers), reads
// the newest $QRP publication record and closes; at the same time its places
// store opens in memory with the $GNP standard. Each context here does both.
// The processor is slowed N-fold for the whole browser (page, workers, network
// and GPU processes): every 20 ms the browser's processes run 20/N ms and are
// stopped for the rest (SIGSTOP/SIGCONT on the processes this script started).
// DevTools' CPU throttling cannot do it: it applies to pages only ("Operation
// is only supported for pages, not workers"), and the threads that must arm are
// workers.
// The runtime files are served with the production site's caching
// (`cache-control: max-age=600`), and the thread-worker script can be delayed
// (`--script-delay-ms`) as the site's origin delays it on a fresh load.
//
// The guest worker is the served `runtime/browser-guest.mjs`, imported by a
// probe module that only watches the workers it creates (when each thread
// worker was created and said it was ready; I/O worker errors) and reports
// them on a BroadcastChannel. It changes nothing the guest does.
//
// A fault: an open that rejects, or a wasm abort (`unreachable`,
// `startThreads`) anywhere. I/O worker start failures
// (`FlatsqlIoWorkerStartError`) are counted separately. Exit 1 on any fault.
//
// Usage: node scripts/catalog-cache-threads.e2e.mjs [--contexts 30] [--cpu 4]
//   [--arming-ms 4000] [--script-delay-ms 0] [--out results.jsonl]
//   [--engine '{"threadArmingMs":60000}']
// --arming-ms only classifies which workers counted as armed in the report.
// --engine adds FlatSQL open options (a measurement of arming time uses a long
// arming time and one attempt); the console passes none.
// Needs `npm run build:core` first and an installed Chromium headless shell.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const args = { contexts: 30, cpu: 4, 'arming-ms': 4000, 'script-delay-ms': 0, out: '', engine: '' };
for (let i = 2; i < process.argv.length; i += 2) {
  const key = process.argv[i].replace(/^--/, '');
  if (!(key in args)) throw new Error(`unknown option ${process.argv[i]}`);
  args[key] = typeof args[key] === 'number' ? Number(process.argv[i + 1]) : process.argv[i + 1];
}
const packageRoot = fileURLToPath(new URL('../', import.meta.url));
const dist = path.join(packageRoot, 'dist');
const schemaRoot = path.join(packageRoot, 'node_modules/spacedatastandards.org/schema');
if (!fs.existsSync(path.join(dist, 'index.mjs'))) throw new Error('Run npm run build:core first');
const notice = fs.readFileSync(path.join(dist, 'runtime/NOTICE.txt'), 'utf8').trim().split('\n')[0];
const flatsqlVersion = JSON.parse(fs.readFileSync(path.join(packageRoot, 'node_modules/flatsql/package.json'), 'utf8')).version;

function shell() {
  if (process.env.GAUNTLET_HEADLESS_SHELL) return process.env.GAUNTLET_HEADLESS_SHELL;
  const cache = process.env.PLAYWRIGHT_BROWSERS_PATH ?? path.join(os.homedir(), process.platform === 'darwin' ? 'Library/Caches/ms-playwright' : '.cache/ms-playwright');
  const dirs = fs.readdirSync(cache).filter(d => /^chromium_headless_shell-\d+$/.test(d)).sort((a, b) => Number(b.split('-')[1]) - Number(a.split('-')[1]));
  for (const dir of dirs) for (const sub of fs.readdirSync(path.join(cache, dir))) {
    const bin = path.join(cache, dir, sub, 'chrome-headless-shell'); if (fs.existsSync(bin)) return bin;
  }
  throw new Error('Install chromium-headless-shell, or set GAUNTLET_HEADLESS_SHELL');
}

const probeWrap = `
const channel = new BroadcastChannel('flatsql-probe');
const guest = Math.random().toString(36).slice(2, 8);
const Base = globalThis.Worker;
let next = 0;
globalThis.Worker = class extends Base {
  constructor(url, options) {
    super(url, options);
    const id = next++, created = performance.now();
    const kind = /wasiThreadBrowserWorker/.test(String(url)) ? 'thread' : /opfsIoWorker/.test(String(url)) ? 'io' : 'other';
    channel.postMessage({ guest, kind, id, event: 'created', at: created });
    this.addEventListener('message', e => {
      if (kind === 'thread' && e.data && e.data.t === 'ready') channel.postMessage({ guest, kind, id, event: 'ready', ok: e.data.ok === true, ms: performance.now() - created });
    });
    this.addEventListener('error', e => channel.postMessage({ guest, kind, id, event: 'error', message: String(e.message || e.error || 'error event without a message'), ms: performance.now() - created }));
  }
};`;
const probeGuest = `import '/probe/wrap.mjs';\nimport '/sdn-js/runtime/browser-guest.mjs';\n`;
const page = `<!doctype html><script type="module">
import * as sdn from '/sdn-js/index.mjs';
const text = async p => { const r = await fetch('/schema/' + p); if (!r.ok) throw new Error('schema ' + p); return r.text(); };
// The console's sdsSchemaText.inlineSdsSchema: includes inlined, each once, root keeps root_type.
const INCLUDE = /^\\s*include\\s+"([^"]+)"\\s*;\\s*$/gm;
async function inline(root) {
  const seen = new Set();
  const visit = async (p, isRoot) => {
    if (seen.has(p)) return ''; seen.add(p);
    const source = await text(p);
    let included = '';
    for (const m of source.matchAll(INCLUDE)) {
      const parts = p.split('/').slice(0, -1);
      for (const part of m[1].split('/')) { if (part === '..') parts.pop(); else if (part !== '.') parts.push(part); }
      included += await visit(parts.join('/'), false);
    }
    let body = source.replace(INCLUDE, '');
    if (!isRoot) body = body.replace(/^\\s*root_type\\s+[^;]+;\\s*$/gm, '').replace(/^\\s*file_identifier\\s+"[^"]*"\\s*;\\s*$/gm, '');
    return included + '\\n' + body;
  };
  return visit(root, true);
}
const probe = [];
new BroadcastChannel('flatsql-probe').onmessage = e => probe.push(e.data);
const engine = { workerUrl: location.origin + '/probe/guest.mjs', ...JSON.parse(new URLSearchParams(location.search).get('engine') || '{}') };
async function timed(open, read) {
  const started = performance.now();
  let store;
  try {
    store = await open();
    const openMs = performance.now() - started;
    const records = await read(store);
    await store.close();
    return { ok: true, openMs, records };
  } catch (error) {
    try { await store?.close(); } catch {}
    return { ok: false, openMs: performance.now() - started, name: error?.name, message: String(error?.message ?? error),
      needed: error?.needed, armed: error?.armed, armedPerAttempt: error?.armedPerAttempt };
  }
}
window.runContext = async () => {
  const standards = [
    { standardId: 'OMM', tableName: 'OMM', fileId: '$OMM', schema: await inline('OMM/main.fbs') },
    { standardId: 'QRP', tableName: 'QRP', fileId: '$QRP', schema: await text('QRP/main.fbs') },
    { standardId: 'PNM', tableName: 'PNM', fileId: '$PNM', schema: await text('PNM/main.fbs') },
  ];
  const gnp = { standardId: 'GNP', tableName: 'GNP', fileId: '$GNP', schema: await text('GNP/main.fbs') };
  const [catalog, places] = await Promise.all([
    timed(() => sdn.FlatSQLEngineRecordStore.open({ persistenceKey: 'spaceaware-catalog', schemas: standards, engine }),
      async store => (await store.query('QRP')).length),
    timed(() => sdn.FlatSQLEngineRecordStore.open({ schemas: [gnp], engine }), async store => (await store.query('GNP')).length),
  ]);
  await new Promise(r => setTimeout(r, 50));
  return { catalog, places, probe };
};
</script>`;

// Requests per runtime file in the current context (contexts run one at a time).
let served = {};
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (url.pathname.startsWith('/sdn-js/runtime/')) served[path.basename(url.pathname)] = (served[path.basename(url.pathname)] ?? 0) + 1;
  res.setHeader('cross-origin-opener-policy', 'same-origin');
  res.setHeader('cross-origin-embedder-policy', 'require-corp');
  res.setHeader('cross-origin-resource-policy', 'same-origin');
  res.setHeader('cache-control', 'no-store');
  const js = 'text/javascript';
  if (url.pathname === '/' ) { res.writeHead(200, { 'content-type': 'text/html' }); res.end(page); return; }
  if (url.pathname === '/probe/wrap.mjs') { res.writeHead(200, { 'content-type': js }); res.end(probeWrap); return; }
  if (url.pathname === '/probe/guest.mjs') { res.writeHead(200, { 'content-type': js }); res.end(probeGuest); return; }
  let file = null;
  if (url.pathname.startsWith('/sdn-js/')) file = path.resolve(dist, '.' + url.pathname.slice('/sdn-js'.length));
  else if (url.pathname.startsWith('/schema/')) file = path.resolve(schemaRoot, '.' + url.pathname.slice('/schema'.length));
  const inside = file && (file.startsWith(dist + '/') || file.startsWith(schemaRoot + '/'));
  if (!inside || !fs.existsSync(file) || !fs.statSync(file).isFile()) { res.writeHead(404); res.end(); return; }
  if (url.pathname.startsWith('/sdn-js/')) res.setHeader('cache-control', 'max-age=600');
  res.setHeader('content-type', { '.mjs': js, '.js': js, '.wasm': 'application/wasm', '.json': 'application/json' }[path.extname(file)] ?? 'text/plain');
  const respond = () => { res.writeHead(200); fs.createReadStream(file).pipe(res); };
  if (args['script-delay-ms'] > 0 && url.pathname === '/sdn-js/runtime/wasiThreadBrowserWorker.mjs') setTimeout(respond, args['script-delay-ms']);
  else respond();
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-catalog-threads-'));
const child = spawn(shell(), ['--remote-debugging-port=0', `--user-data-dir=${profile}`, '--no-first-run', '--no-default-browser-check',
  ...(process.platform === 'linux' ? ['--no-sandbox'] : []), 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
// The N-fold slow-down: the browser's process tree, refreshed every 250 ms.
const tree = new Set([child.pid]);
let slowing = null;
function slowDown(factor) {
  if (factor <= 1) return;
  const period = 20, run = period / factor;
  const signal = sig => { for (const pid of tree) { try { process.kill(pid, sig); } catch { tree.delete(pid); } } };
  const refresh = setInterval(() => {
    const pairs = spawnSync('ps', ['-A', '-o', 'pid=,ppid='], { encoding: 'utf8' }).stdout.trim().split('\n').map(l => l.trim().split(/\s+/).map(Number));
    const alive = new Set(pairs.map(([pid]) => pid));
    for (const pid of tree) if (!alive.has(pid)) tree.delete(pid);  // never signal a reused PID
    for (let grew = true; grew;) { grew = false; for (const [pid, ppid] of pairs) if (tree.has(ppid) && !tree.has(pid)) { tree.add(pid); grew = true; } }
  }, 250);
  let stopped = false;
  const tick = () => {
    if (!slowing) return;
    stopped = !stopped;
    signal(stopped ? 'SIGSTOP' : 'SIGCONT');
    slowing.timer = setTimeout(tick, stopped ? period - run : run);
  };
  slowing = { refresh, timer: null, stop() { clearInterval(refresh); clearTimeout(this.timer); slowing = null; signal('SIGCONT'); } };
  tick();
}
const out = args.out ? fs.createWriteStream(args.out) : null;
const emit = line => { console.log(line); out?.write(line + '\n'); };
const rows = [];
let socket;
try {
  const port = await new Promise((resolve, reject) => {
    let output = ''; const timer = setTimeout(() => reject(new Error('Browser startup timed out')), 20000);
    child.on('error', reject);
    child.stderr.on('data', b => { output += b; const m = output.match(/DevTools listening on ws:\/\/[^:]+:(\d+)\//); if (m) { clearTimeout(timer); resolve(Number(m[1])); } });
    child.on('exit', code => { clearTimeout(timer); reject(new Error(`Browser exited: ${code}`)); });
  });
  const version = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json();
  socket = new WebSocket(version.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  let serial = 0; const pending = new Map();
  let onEvent = () => {};
  socket.onmessage = ({ data }) => {
    const m = JSON.parse(data);
    if (m.id) { const p = pending.get(m.id); pending.delete(m.id); if (m.error) p.reject(new Error(m.error.message)); else p.resolve(m.result); }
    else onEvent(m);
  };
  const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++serial; pending.set(id, { resolve, reject });
    socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
  });
  emit(JSON.stringify({ run: { flatsql: flatsqlVersion, runtime: notice, browser: version.Browser, contexts: args.contexts, cpu: args.cpu, armingMs: args['arming-ms'], scriptDelayMs: args['script-delay-ms'], engine: args.engine || null } }));
  slowDown(args.cpu);
  for (let n = 1; n <= args.contexts; n++) {
    served = {};
    const { browserContextId } = await send('Target.createBrowserContext', { disposeOnDetach: true });
    const { targetId } = await send('Target.createTarget', { url: 'about:blank', browserContextId });
    const errors = [];
    const autoAttach = { autoAttach: true, waitForDebuggerOnStart: true, flatten: true };
    onEvent = m => {
      if (m.method === 'Target.attachedToTarget') {
        const s = m.params.sessionId;
        (async () => {
          await send('Runtime.enable', {}, s).catch(() => {});
          await send('Target.setAutoAttach', autoAttach, s).catch(() => {});
          await send('Runtime.runIfWaitingForDebugger', {}, s).catch(() => {});
        })();
      } else if (m.method === 'Runtime.exceptionThrown') {
        errors.push(m.params.exceptionDetails?.exception?.description ?? m.params.exceptionDetails?.text);
      } else if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
        errors.push(m.params.args.map(a => a.value ?? a.description).join(' '));
      }
    };
    const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
    await send('Runtime.enable', {}, sessionId);
    await send('Page.enable', {}, sessionId);
    await send('Target.setAutoAttach', autoAttach, sessionId);
    await send('Page.navigate', { url: origin + '/' + (args.engine ? '?engine=' + encodeURIComponent(args.engine) : '') }, sessionId);
    const evaluate = async expression => {
      const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }, sessionId);
      if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
      return r.result.value;
    };
    const deadline = Date.now() + 60000;
    while (!(await evaluate('crossOriginIsolated && typeof runContext === "function"').catch(() => false))) {
      if (Date.now() > deadline) throw new Error(`context ${n}: the page did not load cross-origin isolated`);
      await new Promise(r => setTimeout(r, 50));
    }
    const result = await Promise.race([evaluate('runContext()'),
      new Promise((_, reject) => setTimeout(() => reject(new Error(`context ${n}: no answer in 180 s`)), 180000))]);
    // Thread workers per guest, grouped into pool attempts (a new attempt starts
    // after a pause of more than a second between creations).
    const guests = {};
    for (const p of result.probe) {
      const g = guests[p.guest] ??= { threads: new Map(), io: [] };
      if (p.kind === 'thread') {
        const t = g.threads.get(p.id) ?? {}; g.threads.set(p.id, t);
        if (p.event === 'created') t.at = p.at; else if (p.event === 'ready') { t.readyMs = p.ms; t.ok = p.ok; }
      } else if (p.kind === 'io' && p.event === 'error') g.io.push(p.message);
    }
    const pools = Object.values(guests).map(g => {
      const attempts = []; let last = -Infinity;
      for (const t of [...g.threads.values()].sort((a, b) => a.at - b.at)) {
        if (t.at - last > 1000) attempts.push([]);
        attempts.at(-1).push(t); last = t.at;
      }
      return { attempts: attempts.map(a => ({ requested: a.length, armed: a.filter(t => t.ok && t.readyMs <= args['arming-ms']).length,
        slowestReadyMs: Math.round(Math.max(0, ...a.filter(t => t.ok).map(t => t.readyMs))) })), ioErrors: g.io };
    });
    const all = [result.catalog, result.places];
    const engineFault = all.some(r => !r.ok && !/FlatsqlIoWorkerStartError|I\/O worker failed to start/.test(`${r.name} ${r.message}`))
      || errors.some(e => /unreachable|startThreads/.test(e));
    const ioFault = all.some(r => !r.ok && /FlatsqlIoWorkerStartError|I\/O worker failed to start/.test(`${r.name} ${r.message}`));
    const row = { context: n, engineFault, ioFault, catalog: { ...result.catalog, openMs: Math.round(result.catalog.openMs) },
      places: { ...result.places, openMs: Math.round(result.places.openMs) }, pools, runtimeRequests: served,
      pageErrors: errors.filter(e => /unreachable|startThreads|FlatSQL|I\/O worker|wasi-thread/.test(e)).map(e => e.split('\n').slice(0, 3).join(' | ')) };
    rows.push(row);
    emit(JSON.stringify(row));
    onEvent = () => {};
    await send('Target.closeTarget', { targetId });
    await send('Target.disposeBrowserContext', { browserContextId }).catch(() => {});
  }
} finally {
  slowing?.stop();
  socket?.close();
  const exited = new Promise(resolve => { if (child.exitCode !== null || child.signalCode) resolve(); else child.once('exit', resolve); });
  child.kill();
  await Promise.race([exited, new Promise(r => setTimeout(r, 5000))]);
  server.closeAllConnections();
  server.close();
  fs.rmSync(profile, { recursive: true, force: true });
}
const spread = values => {
  const v = values.slice().sort((a, b) => a - b);
  return v.length ? { fastest: v[0], median: v[Math.floor((v.length - 1) / 2)], slowest: v.at(-1) } : null;
};
const opened = key => rows.filter(r => r[key].ok).map(r => r[key].openMs);
const summary = {
  flatsql: flatsqlVersion, runtime: notice, cpu: args.cpu, contexts: rows.length,
  engineFaults: rows.filter(r => r.engineFault).length, ioWorkerFaults: rows.filter(r => r.ioFault).length,
  retriedPools: rows.reduce((n, r) => n + r.pools.filter(p => p.attempts.length > 1).length, 0),
  catalogOpenMs: spread(opened('catalog')), placesOpenMs: spread(opened('places')),
  // Per pool: when its last thread worker was ready, from that worker's creation (first attempt).
  poolArmingMs: spread(rows.flatMap(r => r.pools.filter(p => p.attempts.length).map(p => p.attempts[0].slowestReadyMs))),
};
emit(JSON.stringify({ summary }));
console.log(summary.engineFaults || summary.ioWorkerFaults ? `FAIL: ${summary.engineFaults} engine faults, ${summary.ioWorkerFaults} I/O worker faults in ${rows.length} contexts`
  : `PASS: ${rows.length} contexts at ${args.cpu}x CPU slow-down, no faults`);
out?.end();
if (summary.engineFaults || summary.ioWorkerFaults) process.exit(1);
