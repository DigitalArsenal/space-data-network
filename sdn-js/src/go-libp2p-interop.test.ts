/**
 * THE INTEROP GATE: a real sdn-js client dialling a real go-libp2p host.
 *
 * sdn-js is the browser client for a network whose servers run go-libp2p
 * (sdn-server pins github.com/libp2p/go-libp2p v0.46.0). Until this file
 * existed, EVERY other test in this package either mocked `libp2p` outright
 * (node.test.ts, helia.test.ts, browser-main-thread-safety.test.ts) or replayed
 * recorded server bytes over a stub transport (datasync-wire-compat.test.ts).
 * That means a dependency change could compile, type-check and pass the entire
 * suite while leaving the client unable to open a single connection — the one
 * failure mode that matters most and the only one nothing could see.
 *
 * What this test moves over a real socket, against real go-libp2p:
 *
 *   - the WebSocket transport (`/ip4/127.0.0.1/tcp/<port>/ws`)
 *   - multistream-select for the security protocol, with the Go side offering
 *     BOTH `/tls/1.0.0` and `/noise` so the JS stack has to negotiate
 *   - the `/noise` handshake itself, including secp256k1 identity keys when the
 *     test supplies one
 *   - `/yamux/1.0.0` muxing
 *   - multistream-select for an SDN application protocol
 *   - the SDN request/response stream shape: write one payload, half-close the
 *     write side, read the reply to EOF, close
 *
 * The Go half is `sdn-server/cmd/js-interop-host`. It is compiled from the same
 * Go module as the product server, so it tracks whatever go-libp2p release
 * sdn-server pins rather than a version chosen here.
 *
 * WHEN THIS FAILS, the client cannot talk to the network. Do not weaken it, and
 * do not convert it to a skip: a skip here restores the blind spot it was
 * written to remove. If the Go toolchain is genuinely unavailable the suite
 * reports that as a skip with a reason, and setting SDN_REQUIRE_GO_INTEROP=1
 * turns that skip into a failure for lanes that must have the coverage.
 */

import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { spawn, spawnSync, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { mkdtempSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { SDNNode } from './node';

const HERE = fileURLToPath(new URL('.', import.meta.url));
const SDN_SERVER_DIR = resolve(HERE, '../../sdn-server');
const FIXTURE_PKG = './cmd/js-interop-host';
const REPLY_PREFIX = 'go-libp2p:';

const FLATSQL_SYNC_PROTOCOL = '/space-data-network/flatsql-sync/1.0.0';
const MODULE_DELIVERY_PROTOCOL = '/space-data-network/module-delivery/1.0.0';
const ID_EXCHANGE_PROTOCOL = '/space-data-network/id-exchange/1.0.0';

interface GoHandshake {
  peerId: string;
  addrs: string[];
  wsAddr: string;
  tcpAddr: string;
}

function goToolchainAvailable(): string | null {
  if (!existsSync(join(SDN_SERVER_DIR, 'go.mod'))) {
    return `sdn-server/go.mod not found at ${SDN_SERVER_DIR}`;
  }
  const probe = spawnSync('go', ['version'], { encoding: 'utf8' });
  if (probe.error != null || probe.status !== 0) {
    return `the go toolchain is not runnable: ${probe.error?.message ?? probe.stderr ?? 'unknown'}`;
  }
  return null;
}

const unavailable = goToolchainAvailable();
if (unavailable != null && process.env.SDN_REQUIRE_GO_INTEROP === '1') {
  throw new Error(
    `SDN_REQUIRE_GO_INTEROP=1 but the go-libp2p interop gate cannot run: ${unavailable}`,
  );
}
if (unavailable != null) {
  // Loud, because a silent skip here is indistinguishable from coverage.
  console.warn(
    `[go-libp2p-interop] SKIPPING THE ONLY LIVE INTEROP GATE: ${unavailable}. ` +
      'Set SDN_REQUIRE_GO_INTEROP=1 to make this a hard failure.',
  );
}

const describeInterop = unavailable == null ? describe : describe.skip;

describeInterop('sdn-js dials a live go-libp2p host', () => {
  let workDir: string;
  let hostBin: string;
  let child: ChildProcessWithoutNullStreams | null = null;
  let handshake: GoHandshake;
  let node: SDNNode | null = null;

  beforeAll(async () => {
    workDir = mkdtempSync(join(tmpdir(), 'sdn-js-interop-'));
    hostBin = join(workDir, 'js-interop-host');

    const build = spawnSync('go', ['build', '-o', hostBin, FIXTURE_PKG], {
      cwd: SDN_SERVER_DIR,
      encoding: 'utf8',
    });
    if (build.status !== 0) {
      throw new Error(
        `failed to build ${FIXTURE_PKG}: ${build.stderr || build.stdout || build.error?.message}`,
      );
    }

    handshake = await startGoHost(hostBin);

    node = await SDNNode.create({
      edgeRelays: [handshake.wsAddr],
      includeIPFSBootstrap: false,
      enableStorage: false,
      enableRelayProbing: false,
      // The browser connection gater denies every private-IP dial by default,
      // and 127.0.0.1 is exactly that. Without this the dial fails in a few ms
      // with "denied all addresses" and never reaches a socket.
      allowPrivateAddressDial: true,
    });
  }, 300_000);

  afterAll(async () => {
    try {
      await node?.stop();
    } catch {
      // The node may already be down; the process teardown below is what matters.
    }
    node = null;
    if (child != null) {
      child.stdin.end();
      child.kill('SIGTERM');
      child = null;
    }
    if (workDir != null) {
      rmSync(workDir, { recursive: true, force: true });
    }
  }, 60_000);

  async function startGoHost(bin: string): Promise<GoHandshake> {
    const proc = spawn(bin, [], { stdio: ['pipe', 'pipe', 'pipe'] });
    child = proc;
    let stderr = '';
    proc.stderr.setEncoding('utf8');
    proc.stderr.on('data', (chunk: string) => {
      stderr += chunk;
    });

    return await new Promise<GoHandshake>((resolvePromise, rejectPromise) => {
      let buffered = '';
      const timer = setTimeout(() => {
        rejectPromise(
          new Error(`go host did not announce a listen address in 60s; stderr: ${stderr}`),
        );
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
  }

  it('announces a websocket listen address and a peer id', () => {
    expect(handshake.peerId).toMatch(/^12D3Koo/);
    expect(handshake.wsAddr).toMatch(
      /^\/ip4\/127\.0\.0\.1\/tcp\/\d+\/ws\/p2p\/12D3Koo/,
    );
  });

  it('completes a flatsql-sync request/response over ws + noise + yamux', async () => {
    const payload = new TextEncoder().encode('flatsql-sync-interop-probe');
    const reply = await node!.dialProtocol(
      handshake.peerId,
      FLATSQL_SYNC_PROTOCOL,
      payload,
      [handshake.wsAddr],
    );
    expect(new TextDecoder().decode(reply)).toBe(
      `${REPLY_PREFIX}flatsql-sync-interop-probe`,
    );
  }, 120_000);

  it('completes a module-delivery request/response', async () => {
    const payload = new TextEncoder().encode('module-delivery-interop-probe');
    const reply = await node!.dialProtocol(
      handshake.peerId,
      MODULE_DELIVERY_PROTOCOL,
      payload,
      [handshake.wsAddr],
    );
    expect(new TextDecoder().decode(reply)).toBe(
      `${REPLY_PREFIX}module-delivery-interop-probe`,
    );
  }, 120_000);

  it('completes an id-exchange request/response', async () => {
    const payload = new TextEncoder().encode('ping');
    const reply = await node!.dialProtocol(
      handshake.peerId,
      ID_EXCHANGE_PROTOCOL,
      payload,
      [handshake.wsAddr],
    );
    expect(new TextDecoder().decode(reply)).toBe(`${REPLY_PREFIX}ping`);
  }, 120_000);

  it('carries a payload larger than one yamux window without truncation', async () => {
    // 512 KiB forces multiple yamux frames and at least one window update, which
    // is where a mis-ported write path silently truncates or hangs rather than
    // failing outright.
    const size = 512 * 1024;
    const payload = new Uint8Array(size);
    for (let i = 0; i < size; i++) {
      payload[i] = (i * 31 + 7) & 0xff;
    }
    const reply = await node!.dialProtocol(
      handshake.peerId,
      FLATSQL_SYNC_PROTOCOL,
      payload,
      [handshake.wsAddr],
    );
    expect(reply.byteLength).toBe(REPLY_PREFIX.length + size);
    expect(new TextDecoder().decode(reply.subarray(0, REPLY_PREFIX.length))).toBe(
      REPLY_PREFIX,
    );
    expect(reply.subarray(REPLY_PREFIX.length)).toEqual(payload);
  }, 180_000);

  it('records the go-libp2p peer as a connected peer', () => {
    expect(node!.peers).toContain(handshake.peerId);
  });
});
