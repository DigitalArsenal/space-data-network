import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { decodeObservedSDNPeers, requestObservedSDNPeers, PEER_DIRECTORY_PROTOCOL_ID } from './peer-directory';

it('reads the real Go directory E2E page and preserves its bounded typed contract', async () => {
  const bytes = new Uint8Array(readFileSync(new URL('./testdata/peer-directory.qrp', import.meta.url)));
  const decoded = decodeObservedSDNPeers(bytes);
  expect(decoded.totalCount).toBe(2);
  expect(decoded.peers).toHaveLength(1);
  expect(decoded.peers[0]).toMatchObject({ connected: true, evidence: 'connected', agent: 'spacedatanetwork/directory-e2e',
    protocols: ['/spacedatanetwork/epm-exchange/1.0.0'] });
  expect(decoded.peers[0].multiaddrs).toHaveLength(1);
  expect(decoded.peers[0].multiaddrs[0]).toMatch(/^\/ip4\/(8\.8\.8\.8|1\.1\.1\.1)\/tcp\/443\/tls\/ws$/);
  const signal = new AbortController().signal;
  const page = await requestObservedSDNPeers({ dialProtocol: async (target, protocol, payload, addrs, options) => {
    expect(target).toBe(decoded.reporterPeerId);
    expect(protocol).toBe(PEER_DIRECTORY_PROTOCOL_ID);
    expect(payload.length).toBeGreaterThan(12);
    expect(options).toEqual({ signal, maxBytes: 1048576 });
    return bytes;
  } }, { targetPeerId: decoded.reporterPeerId, signal, limit: 1 });
  expect(page).toEqual(decoded);
  expect(() => decodeObservedSDNPeers(bytes.subarray(0, bytes.length - 1))).toThrow('Invalid directory frame');
});
