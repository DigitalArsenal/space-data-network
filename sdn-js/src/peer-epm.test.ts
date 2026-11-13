import { it, expect } from 'vitest';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { once } from 'node:events';
import { createLibp2p } from 'libp2p';
import { tcp } from '@libp2p/tcp';
import { noise } from '@libp2p/noise';
import { yamux } from '@libp2p/yamux';
import { multiaddr } from '@multiformats/multiaddr';
import { requestPeerEpm } from './peer-epm';

it('exchanges real node EPMs and rejects malformed, oversized and canceled streams', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'peer-epm-e2e-'));
  const bin = join(dir, 'epm-host');
  const build = spawn('go', ['test', '-tags=noplugin', '-c', '-o', bin, './internal/epm'], { cwd: new URL('../../sdn-server', import.meta.url) });
  let output = '';
  build.stdout.on('data', data => { output += data; });
  build.stderr.on('data', data => { output += data; });
  const [code] = await once(build, 'close');
  expect(code, output).toBe(0);
  const child = spawn(bin, ['-test.run=^TestPeerEPMInteropHost$', '-test.timeout=2m'], { env: { ...process.env, SDN_EPM_INTEROP_DIR: dir } });
  const exited = once(child, 'close');
  child.stdout.on('data', data => { output += data; });
  child.stderr.on('data', data => { output += data; });
  const node = await createLibp2p({ transports: [tcp()], connectionEncrypters: [noise()], streamMuxers: [yamux()] });
  try {
    let valid = '', invalid = '';
    for (let attempt = 0; attempt < 200; attempt++) {
      if (child.exitCode !== null) throw new Error(output);
      try {
        valid = await readFile(join(dir, 'valid.addr'), 'utf8');
        invalid = await readFile(join(dir, 'invalid.addr'), 'utf8');
        break;
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
        await delay(25);
      }
    }
    expect(valid, output).not.toBe('');
    const targetPeerId = valid.split('/p2p/')[1];
    await node.dial(multiaddr(valid));
    const expected = new Uint8Array(await readFile(join(dir, 'profile.epm')));
    expect(await requestPeerEpm(node, { targetPeerId })).toEqual(expected);
    expect(await requestPeerEpm(node, { targetPeerId, subjectPeerId: targetPeerId })).toEqual(expected);
    await expect(requestPeerEpm(node, { targetPeerId, subjectPeerId: node.peerId.toString() })).rejects.toThrow('not found');
    await expect(requestPeerEpm(node, { targetPeerId, maxBytes: 1 })).rejects.toThrow('byte limit');
    await node.dial(multiaddr(invalid));
    const badPeer = invalid.split('/p2p/')[1];
    for (const error of ['header is truncated', 'Unknown peer profile status 9', 'byte limit', 'payload is truncated', 'trailing bytes', 'empty', 'error response carries a payload', 'request failed']) {
      await expect(requestPeerEpm(node, { targetPeerId: badPeer })).rejects.toThrow(error);
    }
    const controller = new AbortController();
    const pending = requestPeerEpm(node, { targetPeerId: badPeer, signal: controller.signal });
    const canceled = expect(pending).rejects.toThrow();
    for (let attempt = 0; ; attempt++) {
      try { await stat(join(dir, 'cancel-ready')); break; }
      catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'ENOENT' || attempt === 199) throw error;
        await delay(25);
      }
    }
    controller.abort(new Error('E2E canceled'));
    await canceled;
  } finally {
    await node.stop();
    child.stdin.end();
    const [status] = await exited;
    expect(status, output).toBe(0);
    await rm(dir, { recursive: true, force: true });
  }
}, 180000);
