import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';

import { NodeStatus, NodeStatusSet } from './generated/nst.js';
import { decodeNodeStatusSet, nodeStatusSetToView } from './view-model';

/**
 * Build a size-prefixed `$NST` NodeStatusSet buffer with the generated
 * FlatBuffers builder — the same code path the Go node uses to emit frames.
 */
function buildNodeStatusSetBytes(): Uint8Array {
  const builder = new flatbuffers.Builder(1024);

  // --- node 0: self ---
  const peerId0 = builder.createString('12D3KooWSelfNode');
  const dn0 = builder.createString('CN=Self Node,O=SpaceAware');
  const org0 = builder.createString('SpaceAware');
  const trust0 = builder.createString('high');
  const role0 = builder.createString('self');
  const agent0 = builder.createString('sdn-js/1.0.4');
  const addr0a = builder.createString('/dns4/sdn.spaceaware.io/tcp/443/wss');
  const addr0b = builder.createString('/ip4/10.0.0.1/tcp/4001');
  const addrs0 = NodeStatus.createMultiformatAddressVector(builder, [addr0a, addr0b]);
  const vcard0 = builder.createString('BEGIN:VCARD\nVERSION:4.0\nFN:Self\nEND:VCARD');
  const country0 = builder.createString('US');
  const city0 = builder.createString('New York');
  const suite0 = builder.createString('1.0.4');
  const standards0 = builder.createString('1.155.0');

  NodeStatus.startNodeStatus(builder);
  NodeStatus.addPeerId(builder, peerId0);
  NodeStatus.addDn(builder, dn0);
  NodeStatus.addOrganization(builder, org0);
  NodeStatus.addTrustLevel(builder, trust0);
  NodeStatus.addRole(builder, role0);
  NodeStatus.addAgentVersion(builder, agent0);
  NodeStatus.addMultiformatAddress(builder, addrs0);
  NodeStatus.addLastSeen(builder, BigInt(1_700_000_000));
  NodeStatus.addIsOnline(builder, true);
  NodeStatus.addLatencyMs(builder, 42.5);
  NodeStatus.addVcard(builder, vcard0);
  NodeStatus.addLat(builder, 40.7128);
  NodeStatus.addLon(builder, -74.006);
  NodeStatus.addGeoCountry(builder, country0);
  NodeStatus.addGeoCity(builder, city0);
  NodeStatus.addIsSelf(builder, true);
  NodeStatus.addUptimeS(builder, BigInt(3600));
  NodeStatus.addSuiteVersion(builder, suite0);
  NodeStatus.addStandardsVersion(builder, standards0);
  const node0 = NodeStatus.endNodeStatus(builder);

  // --- node 1: a peer with no geo, offline ---
  const peerId1 = builder.createString('12D3KooWPeerTwo');
  NodeStatus.startNodeStatus(builder);
  NodeStatus.addPeerId(builder, peerId1);
  NodeStatus.addIsOnline(builder, false);
  NodeStatus.addIsSelf(builder, false);
  const node1 = NodeStatus.endNodeStatus(builder);

  const sourcePeerId = builder.createString('12D3KooWSelfNode');
  const nodesVector = NodeStatusSet.createNodesVector(builder, [node0, node1]);
  NodeStatusSet.startNodeStatusSet(builder);
  NodeStatusSet.addNodes(builder, nodesVector);
  NodeStatusSet.addGeneratedAt(builder, BigInt(1_700_000_123_456));
  NodeStatusSet.addSourcePeerId(builder, sourcePeerId);
  const setOffset = NodeStatusSet.endNodeStatusSet(builder);
  NodeStatusSet.finishSizePrefixedNodeStatusSetBuffer(builder, setOffset);

  return builder.asUint8Array().slice();
}

describe('status view model', () => {
  it('carries the $NST size-prefixed file identifier', () => {
    const bytes = buildNodeStatusSetBytes();
    const bb = new flatbuffers.ByteBuffer(bytes);
    bb.setPosition(bb.position() + flatbuffers.SIZE_PREFIX_LENGTH);
    expect(NodeStatusSet.bufferHasIdentifier(bb)).toBe(true);
  });

  it('decodes size-prefixed NodeStatusSet bytes to the plain view model', () => {
    const view = decodeNodeStatusSet(buildNodeStatusSetBytes());

    expect(view.sourcePeerId).toBe('12D3KooWSelfNode');
    expect(view.generatedAt).toBe(1_700_000_123_456);
    expect(view.nodes).toHaveLength(2);
  });

  it('maps every UPPER_SNAKE field to its camelCase view property', () => {
    const [self] = decodeNodeStatusSet(buildNodeStatusSetBytes()).nodes;

    expect(self.peerId).toBe('12D3KooWSelfNode');
    expect(self.dn).toBe('CN=Self Node,O=SpaceAware');
    expect(self.org).toBe('SpaceAware');
    expect(self.trustLevel).toBe('high');
    expect(self.role).toBe('self');
    expect(self.agent).toBe('sdn-js/1.0.4');
    expect(self.addrs).toEqual([
      '/dns4/sdn.spaceaware.io/tcp/443/wss',
      '/ip4/10.0.0.1/tcp/4001',
    ]);
    expect(self.lastSeen).toBe(1_700_000_000);
    expect(self.online).toBe(true);
    expect(self.latencyMs).toBeCloseTo(42.5, 5);
    expect(self.vcard).toContain('BEGIN:VCARD');
    expect(self.lat).toBeCloseTo(40.7128, 2);
    expect(self.lon).toBeCloseTo(-74.006, 2);
    expect(self.geoLabel).toBe('New York, US');
    expect(self.isSelf).toBe(true);
    expect(self.uptimeS).toBe(3600);
    expect(self.suiteVersion).toBe('1.0.4');
    expect(self.standardsVersion).toBe('1.155.0');
  });

  it('applies safe defaults for absent fields (fail-open decode)', () => {
    const [, peer] = decodeNodeStatusSet(buildNodeStatusSetBytes()).nodes;

    expect(peer.peerId).toBe('12D3KooWPeerTwo');
    expect(peer.online).toBe(false);
    expect(peer.isSelf).toBe(false);
    expect(peer.dn).toBe('');
    expect(peer.org).toBe('');
    expect(peer.vcard).toBe('');
    expect(peer.geoLabel).toBe('');
    expect(peer.addrs).toEqual([]);
    expect(peer.lat).toBe(0);
    expect(peer.lon).toBe(0);
    expect(peer.uptimeS).toBe(0);
    expect(peer.lastSeen).toBe(0);
  });

  it('nodeStatusSetToView returns numeric (not bigint) timestamps', () => {
    const bb = new flatbuffers.ByteBuffer(buildNodeStatusSetBytes());
    const set = NodeStatusSet.getSizePrefixedRootAsNodeStatusSet(bb);
    const view = nodeStatusSetToView(set);

    expect(typeof view.generatedAt).toBe('number');
    expect(typeof view.nodes[0].uptimeS).toBe('number');
    expect(typeof view.nodes[0].lastSeen).toBe('number');
  });
});
