import { describe, expect, it, vi } from 'vitest';
import type { SDNNode } from './node';

vi.mock('./license', () => ({
  LICENSE_PROTOCOL_ID: '/space-data-network/license/1.0.0',
  requestLicenseGrantViaRelay: vi.fn(),
}));

interface SpaceAwareNodeInfo {
  peer_id?: unknown;
  listen_addresses?: unknown;
}

const SPACEAWARE_NODE_INFO_URL =
  process.env.SDN_SPACEAWARE_NODE_INFO_URL ?? 'https://spaceaware.io/api/node/info';
const SPACEAWARE_DNS_RELAY = process.env.SDN_SPACEAWARE_DNS_RELAY ?? 'spaceaware.io';

const runLiveRelayTest = process.env.SDN_RUN_RELAY_TEST === '1';
const describeLive = runLiveRelayTest ? describe : describe.skip;

describeLive('spaceaware relay integration', () => {
  it('dials a live relay address from spaceaware.io deployment', { timeout: 120_000 }, async () => {
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
  const info = await fetchNodeInfo();
  const peerId = asNonEmptyString(info.peer_id, 'peer_id');
  const listenAddresses = asStringArray(info.listen_addresses);

  const candidates = new Set<string>();
  candidates.add(`/dns4/${SPACEAWARE_DNS_RELAY}/tcp/443/wss/p2p/${peerId}`);

  for (const addr of listenAddresses) {
    if (addr.includes('/ws')) {
      candidates.add(ensurePeerSuffix(addr, peerId));
    }
  }

  return { peerId, candidates: Array.from(candidates) };
}

async function fetchNodeInfo(): Promise<SpaceAwareNodeInfo> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 15_000);

  try {
    const response = await fetch(SPACEAWARE_NODE_INFO_URL, { signal: controller.signal });
    if (!response.ok) {
      throw new Error(`spaceaware node info request failed: HTTP ${response.status}`);
    }
    return (await response.json()) as SpaceAwareNodeInfo;
  } finally {
    clearTimeout(timeout);
  }
}

function asNonEmptyString(value: unknown, fieldName: string): string {
  if (typeof value !== 'string' || value.trim().length === 0) {
    throw new Error(`spaceaware node info missing "${fieldName}"`);
  }
  return value;
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0);
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
