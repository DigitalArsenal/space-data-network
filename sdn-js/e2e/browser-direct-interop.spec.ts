/**
 * A REAL BROWSER dials a REAL go-libp2p host over the two transports a browser
 * can use with no relay in the middle: webtransport and webrtc-direct.
 *
 * WHY THIS EXISTS, given src/go-libp2p-interop.test.ts already dials a Go host.
 * That test runs under Node, over a websocket. A browser cannot do what it does:
 * it has no TCP, and a node's websocket listener is plain ws on a raw port,
 * which a page on https may not touch. The unrelayed paths from a browser to an
 * SDN node are webtransport and webrtc-direct, and BOTH are built on APIs that
 * do not exist in Node at all — WebTransport and RTCPeerConnection — and on
 * certificate-hash authentication that only a browser enforces. Node interop
 * passing is therefore no evidence whatsoever that a browser can connect.
 *
 * What it dials is the PUBLISHED artifact: dist/index.mjs, the browser-platform
 * bundle the npm package ships, imported by a page over http. Not the
 * TypeScript sources, not a test-only build.
 *
 * The Go side is cmd/js-interop-host, which builds its host from
 * node.HostTransportOptions — the node's own transport list — so the addresses
 * dialed here are the kind a real node announces, certhashes and all.
 */
import { expect, test, type Page } from '@playwright/test';
import { spawn, spawnSync, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { createReadStream, existsSync, mkdtempSync, rmSync, statSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { tmpdir } from 'node:os';
import { extname, join, normalize, resolve } from 'node:path';

// Playwright transpiles this file to CommonJS (sdn-js has no "type": "module"),
// so __dirname is what resolves paths here — import.meta does not exist.
const SDN_JS_DIR = resolve(__dirname, '..');
const SDN_SERVER_DIR = resolve(SDN_JS_DIR, '../sdn-server');
const BUNDLE = join(SDN_JS_DIR, 'dist/index.mjs');
const FIXTURE_PKG = './cmd/js-interop-host';

const REPLY_PREFIX = 'go-libp2p:';
const FLATSQL_SYNC_PROTOCOL = '/space-data-network/flatsql-sync/1.0.0';

interface GoHandshake {
  peerId: string;
  addrs: string[];
  wsAddr: string;
  tcpAddr: string;
  webTransportAddr: string;
  webRtcDirectAddr: string;
}

// A skip here is indistinguishable from coverage unless it says so, and this is
// the only gate that runs the shipped bundle in a browser engine.
function unavailableReason(): string | null {
  if (!existsSync(BUNDLE)) {
    return `${BUNDLE} is missing — run: npm run build:core`;
  }
  if (!existsSync(join(SDN_SERVER_DIR, 'go.mod'))) {
    return `sdn-server/go.mod not found at ${SDN_SERVER_DIR}`;
  }
  const probe = spawnSync('go', ['version'], { encoding: 'utf8' });
  if (probe.error != null || probe.status !== 0) {
    return `the go toolchain is not runnable: ${probe.error?.message ?? probe.stderr ?? 'unknown'}`;
  }
  return null;
}

const unavailable = unavailableReason();
if (unavailable != null && process.env.SDN_REQUIRE_BROWSER_INTEROP === '1') {
  throw new Error(`SDN_REQUIRE_BROWSER_INTEROP=1 but the browser interop gate cannot run: ${unavailable}`);
}

const MIME: Record<string, string> = {
  '.mjs': 'text/javascript',
  '.js': 'text/javascript',
  '.wasm': 'application/wasm',
  '.html': 'text/html',
  '.json': 'application/json',
  '.map': 'application/json',
};

/**
 * Serves sdn-js over http on 127.0.0.1, which browsers treat as a secure
 * context — WebTransport and RTCPeerConnection both refuse to run outside one,
 * so this cannot be a file:// page.
 */
async function serveSdnJs(): Promise<{ server: Server; origin: string }> {
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1');
    if (url.pathname === '/' || url.pathname === '/index.html') {
      res.writeHead(200, { 'content-type': 'text/html' });
      res.end('<!doctype html><meta charset="utf-8"><title>sdn-js browser interop</title>');
      return;
    }
    const filePath = normalize(join(SDN_JS_DIR, url.pathname));
    if (!filePath.startsWith(SDN_JS_DIR) || !existsSync(filePath) || !statSync(filePath).isFile()) {
      res.writeHead(404).end('not found');
      return;
    }
    res.writeHead(200, { 'content-type': MIME[extname(filePath)] ?? 'application/octet-stream' });
    createReadStream(filePath).pipe(res);
  });
  await new Promise<void>((done) => server.listen(0, '127.0.0.1', done));
  const address = server.address();
  if (address == null || typeof address === 'string') throw new Error('http server has no port');
  return { server, origin: `http://127.0.0.1:${address.port}` };
}

async function startGoHost(bin: string): Promise<{ proc: ChildProcessWithoutNullStreams; handshake: GoHandshake }> {
  const proc = spawn(bin, [], { stdio: ['pipe', 'pipe', 'pipe'] });
  let stderr = '';
  proc.stderr.setEncoding('utf8');
  proc.stderr.on('data', (chunk: string) => {
    stderr += chunk;
  });
  const handshake = await new Promise<GoHandshake>((resolvePromise, rejectPromise) => {
    let buffered = '';
    const timer = setTimeout(() => {
      rejectPromise(new Error(`go host did not announce its addresses in 60s; stderr: ${stderr}`));
    }, 60_000);
    proc.stdout.setEncoding('utf8');
    proc.stdout.on('data', (chunk: string) => {
      buffered += chunk;
      const newline = buffered.indexOf('\n');
      if (newline === -1) return;
      clearTimeout(timer);
      try {
        resolvePromise(JSON.parse(buffered.slice(0, newline)) as GoHandshake);
      } catch (error) {
        rejectPromise(error as Error);
      }
    });
    proc.on('exit', (code) => {
      clearTimeout(timer);
      rejectPromise(new Error(`go host exited early with code ${code}; stderr: ${stderr}`));
    });
  });
  return { proc, handshake };
}

/**
 * Runs entirely INSIDE the browser: imports the shipped bundle, brings up an
 * SDNNode, and does one request/response over the given address.
 *
 * Returns the decoded reply, or the error text, because a rejection crossing
 * the evaluate boundary arrives without a usable message.
 */
async function dialFromBrowser(
  page: Page,
  bundleUrl: string,
  addr: string,
  peerId: string,
  payloadText: string,
): Promise<{ ok: true; reply: string; usedWebTransport: number; usedWebRtc: number } | { ok: false; error: string }> {
  return await page.evaluate(
    async ({ bundleUrl, addr, peerId, payloadText, protocol }) => {
      // A round-trip alone does not prove WHICH transport carried it, and
      // "the browser connected" is the entire claim. Counting constructions of
      // the two browser APIs settles it at the engine: neither exists in Node,
      // and neither can be reached except by actually using that transport.
      let usedWebTransport = 0;
      let usedWebRtc = 0;
      const RealWebTransport = (globalThis as any).WebTransport;
      const RealRTCPeerConnection = (globalThis as any).RTCPeerConnection;
      if (typeof RealWebTransport === 'function') {
        (globalThis as any).WebTransport = new Proxy(RealWebTransport, {
          construct(target, args) {
            usedWebTransport += 1;
            return Reflect.construct(target, args);
          },
        });
      }
      if (typeof RealRTCPeerConnection === 'function') {
        (globalThis as any).RTCPeerConnection = new Proxy(RealRTCPeerConnection, {
          construct(target, args) {
            usedWebRtc += 1;
            return Reflect.construct(target, args);
          },
        });
      }
      try {
        const { SDNNode } = await import(/* @vite-ignore */ bundleUrl);
        const node = await SDNNode.create({
          edgeRelays: [addr],
          includeIPFSBootstrap: false,
          enableStorage: false,
          enableRelayProbing: false,
          // 127.0.0.1 is a private address, and the browser gater denies those
          // by default — without this the dial fails in milliseconds without
          // ever opening a socket.
          allowPrivateAddressDial: true,
        });
        try {
          const reply = await node.dialProtocol(
            peerId,
            protocol,
            new TextEncoder().encode(payloadText),
            [addr],
          );
          return { ok: true as const, reply: new TextDecoder().decode(reply), usedWebTransport, usedWebRtc };
        } finally {
          await node.stop().catch(() => {});
        }
      } catch (error) {
        return { ok: false as const, error: String((error as Error)?.stack ?? error) };
      } finally {
        (globalThis as any).WebTransport = RealWebTransport;
        (globalThis as any).RTCPeerConnection = RealRTCPeerConnection;
      }
    },
    { bundleUrl, addr, peerId, payloadText, protocol: FLATSQL_SYNC_PROTOCOL },
  );
}

test.describe('a browser dials a go-libp2p host', () => {
  test.skip(unavailable != null, unavailable ?? '');

  let workDir: string;
  let proc: ChildProcessWithoutNullStreams | null = null;
  let server: Server | null = null;
  let origin = '';
  let handshake: GoHandshake;

  test.beforeAll(async () => {
    workDir = mkdtempSync(join(tmpdir(), 'sdn-js-browser-interop-'));
    const bin = join(workDir, 'js-interop-host');
    const build = spawnSync('go', ['build', '-o', bin, FIXTURE_PKG], {
      cwd: SDN_SERVER_DIR,
      encoding: 'utf8',
    });
    if (build.status !== 0) {
      throw new Error(`failed to build ${FIXTURE_PKG}: ${build.stderr || build.stdout || build.error?.message}`);
    }
    ({ proc, handshake } = await startGoHost(bin));
    ({ server, origin } = await serveSdnJs());
  });

  test.afterAll(async () => {
    if (proc != null) {
      proc.stdin.end();
      proc.kill('SIGTERM');
      proc = null;
    }
    if (server != null) {
      await new Promise<void>((done) => server!.close(() => done()));
      server = null;
    }
    if (workDir != null) rmSync(workDir, { recursive: true, force: true });
  });

  test('the host announces both direct addresses, with certhashes', () => {
    expect(handshake.webTransportAddr).toMatch(
      /^\/ip4\/127\.0\.0\.1\/udp\/\d+\/quic-v1\/webtransport\/certhash\/[A-Za-z0-9_-]+/,
    );
    expect(handshake.webRtcDirectAddr).toMatch(
      /^\/ip4\/127\.0\.0\.1\/udp\/\d+\/webrtc-direct\/certhash\/[A-Za-z0-9_-]+/,
    );
  });

  test('webtransport: request and response complete in the browser', async ({ page }) => {
    await page.goto(`${origin}/`);
    const result = await dialFromBrowser(
      page,
      `${origin}/dist/index.mjs`,
      handshake.webTransportAddr,
      handshake.peerId,
      'webtransport-browser-probe',
    );
    expect(result.ok ? '' : result.error).toBe('');
    expect(result.ok && result.reply).toBe(`${REPLY_PREFIX}webtransport-browser-probe`);
    // The WebTransport constructor is a browser API with no Node equivalent,
    // so a non-zero count is proof the bytes went over webtransport and not
    // over some fallback the client chose on its own.
    expect(result.ok && result.usedWebTransport).toBeGreaterThan(0);
    expect(result.ok && result.usedWebRtc).toBe(0);
  });

  // The path production ACTUALLY uses today. sdn.spaceaware.io announces only
  // /tcp/4004/ws, /tcp/18080/ws and /dns4/sdn.spaceaware.io/tcp/443/wss — nginx
  // terminates TLS on 443 and proxies the websocket to the node — so until a
  // node exposes UDP directly, every browser reaches SDN over a websocket.
  // Covering the two direct transports and not this one would gate the paths
  // nobody is on and leave the one everybody is on untested in a browser.
  test('websocket: request and response complete in the browser', async ({ page }) => {
    await page.goto(`${origin}/`);
    const result = await dialFromBrowser(
      page,
      `${origin}/dist/index.mjs`,
      handshake.wsAddr,
      handshake.peerId,
      'websocket-browser-probe',
    );
    expect(result.ok ? '' : result.error).toBe('');
    expect(result.ok && result.reply).toBe(`${REPLY_PREFIX}websocket-browser-probe`);
    // Neither direct transport may be constructed: a websocket dial that
    // quietly upgraded to one of them would prove the wrong thing.
    expect(result.ok && result.usedWebTransport).toBe(0);
    expect(result.ok && result.usedWebRtc).toBe(0);
  });

  test('webrtc-direct: request and response complete in the browser', async ({ page }) => {
    await page.goto(`${origin}/`);
    const result = await dialFromBrowser(
      page,
      `${origin}/dist/index.mjs`,
      handshake.webRtcDirectAddr,
      handshake.peerId,
      'webrtc-direct-browser-probe',
    );
    expect(result.ok ? '' : result.error).toBe('');
    expect(result.ok && result.reply).toBe(`${REPLY_PREFIX}webrtc-direct-browser-probe`);
    expect(result.ok && result.usedWebRtc).toBeGreaterThan(0);
    expect(result.ok && result.usedWebTransport).toBe(0);
  });
});
