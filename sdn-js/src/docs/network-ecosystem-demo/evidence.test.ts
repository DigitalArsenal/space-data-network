import { describe, expect, it } from 'vitest';

import {
  buildSandboxChannelEvidence,
  buildSandboxPnmEvidence,
  createSandboxArtifactEvidence,
  requestLiveConnections,
} from './evidence';
import { verifySignedPnm } from '../../pnm-publisher';

describe('network ecosystem evidence', () => {
  it('creates signed sandbox artifact evidence with a verifiable signature', async () => {
    const evidence = await createSandboxArtifactEvidence({
      schema: 'OMM',
      title: 'Demo OMM',
      payload: { objectId: '25544', epoch: '2026-07-08T00:00:00.000Z' },
    });

    expect(evidence.schema).toBe('OMM');
    expect(evidence.cid).toMatch(/^bafyecosystem[0-9a-f]{24}$/);
    expect(evidence.signatureHex).toMatch(/^[0-9a-f]{128}$/);
    expect(evidence.publicKeyHex).toMatch(/^[0-9a-f]+$/);
    expect(evidence.verified).toBe(true);
  });

  it('builds a signed PNM using the canonical sdn-js helper', async () => {
    const artifact = await createSandboxArtifactEvidence({
      schema: 'DPM',
      title: 'Demo DPM',
      payload: { provider: 'celestrak.eth' },
    });
    const pnm = await buildSandboxPnmEvidence(artifact);
    const verified = await verifySignedPnm(pnm.bytes, pnm.publicKey);

    expect(pnm.topic).toBe('/spacedatanetwork/sds/PNM.fbs');
    expect(verified.cid).toBe(artifact.cid);
    expect(verified.fileId).toBe('$DPM');
  });

  it('uses the canonical channel topic format', () => {
    expect(buildSandboxChannelEvidence({ sourceId: 'celestrak-eth', standardCode: 'OMM' })).toEqual({
      channelId: 'celestrak-eth-OMM',
      topic: '/spacedatanetwork/channels/OMM',
      sourceId: 'celestrak-eth',
      standardCode: 'OMM',
    });
  });

  it('does not perform live network work in v1', async () => {
    const result = await requestLiveConnections();

    expect(result.mode).toBe('live');
    expect(result.connections).toEqual([
      {
        id: 'sdn.spaceaware.io',
        status: 'unavailable',
        detail: 'Live provider fetch is not wired in this docs demo version.',
      },
      {
        id: 'celestrak.eth',
        status: 'unavailable',
        detail: 'Live provider fetch is not wired in this docs demo version.',
      },
    ]);
  });
});
