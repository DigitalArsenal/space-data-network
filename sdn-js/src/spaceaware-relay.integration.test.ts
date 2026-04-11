import { describe, expect, it } from 'vitest';
import type { SDNNode } from './node';
import { deriveProviderPeerId } from './discovery';

const SPACEAWARE_PROVIDER_PUBLIC_KEY = process.env.SDN_SPACEAWARE_PROVIDER_PUBLIC_KEY ?? '';
const SPACEAWARE_RELAY_CANDIDATES = (process.env.SDN_SPACEAWARE_RELAY_CANDIDATES ?? '')
  .split(',')
  .map((value) => value.trim())
  .filter(Boolean);

const runLiveRelayTest = process.env.SDN_RUN_RELAY_TEST === '1';
const hasRelayFixture = SPACEAWARE_PROVIDER_PUBLIC_KEY.length > 0 && SPACEAWARE_RELAY_CANDIDATES.length > 0;
const describeLive = runLiveRelayTest && hasRelayFixture ? describe : describe.skip;

describeLive('spaceaware relay integration', () => {
  it('dials a live relay address from a runtime-supplied provider descriptor', { timeout: 120_000 }, async () => {
    const { SDNNode } = await import('./node');
    const { peerId, candidates } = await resolveRelayCandidates();
    expect(candidates.length).toBeGreaterThan(0);

    const node = await SDNNode.create({
      edgeRelays: candidates,
      includeIPFSBootstrap: false,
      enableStorage: false,
    });

    try {
      await dialFirstReachableRelay(node, candidates);
      await waitForPeer(node, peerId, 10_000);
      expect(node.peers).toContain(peerId);
    } finally {
      await node.stop();
    }
  });
});

async function resolveRelayCandidates(): Promise<{ peerId: string; candidates: string[] }> {
  const peerId = await deriveProviderPeerId(hexToBytes(SPACEAWARE_PROVIDER_PUBLIC_KEY));
  const candidates = SPACEAWARE_RELAY_CANDIDATES.map((addr) => ensurePeerSuffix(addr, peerId));
  return { peerId, candidates };
}

function hexToBytes(hex: string): Uint8Array {
  const normalized = hex.trim().toLowerCase();
  if (normalized.length % 2 !== 0) {
    throw new Error('provider public key must be hex');
  }
  const bytes = new Uint8Array(normalized.length / 2);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(normalized.slice(index * 2, index * 2 + 2), 16);
  }
  return bytes;
}

function ensurePeerSuffix(addr: string, peerId: string): string {
  return addr.includes('/p2p/') ? addr : `${addr}/p2p/${peerId}`;
}

async function dialFirstReachableRelay(node: SDNNode, candidates: string[]): Promise<void> {
  let lastErr: unknown = null;

  for (const relayAddr of candidates) {
    try {
      await node.dial(relayAddr);
      return;
    } catch (err) {
      lastErr = err;
    }
  }

  throw new Error(
    `failed to dial any spaceaware relay candidate (${candidates.length}): ${formatError(lastErr)}`
  );
}

async function waitForPeer(node: SDNNode, peerId: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;

  while (Date.now() < deadline) {
    if (node.peers.includes(peerId)) {
      return;
    }
    await sleep(250);
  }

  throw new Error(`relay peer ${peerId} not visible in connected peers: ${node.peers.join(', ')}`);
}

async function sleep(ms: number): Promise<void> {
  await new Promise<void>((resolve) => {
    setTimeout(resolve, ms);
  });
}

function formatError(err: unknown): string {
  if (err instanceof Error) {
    return err.message;
  }
  return String(err);
}
