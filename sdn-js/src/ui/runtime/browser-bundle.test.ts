import { encodePluginManifest } from '../../../node_modules/space-data-module-sdk/src/manifest/browser.js';
import {
  appendPublicationRecordCollection,
  encodePublicationRecordCollection,
} from '../../../node_modules/space-data-module-sdk/src/transport/records.js';
import { describe, expect, it } from 'vitest';

import {
  parseBrowserBundle,
  parseFirstBrowserBundle,
} from '../../../ui/src/browser-bundle';

const WASM_HEADER = Uint8Array.of(0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00);

describe('parseBrowserBundle', () => {
  it('reads the REC trailer MBL record and decodes the embedded manifest', async () => {
    const manifestBytes = encodePluginManifest({
      pluginId: 'com.space-data-network.live-demo',
      name: 'Live Demo',
      version: '0.1.0',
      invokeSurfaces: ['direct'],
      runtimeTargets: ['browser'],
    });
    const canonicalHash = Uint8Array.from(
      Array.from({ length: 32 }, (_, index) => index + 1),
    );
    const recordCollectionBytes = encodePublicationRecordCollection({
      mbl: {
        canonicalModuleHash: canonicalHash,
        entries: [
          {
            entryId: 'manifest',
            role: 'manifest',
            sectionName: 'sds.manifest',
            payloadEncoding: 'flatbuffer',
            typeRef: {
              schemaName: 'PluginManifest.fbs',
              fileIdentifier: 'PMAN',
            },
            payload: manifestBytes,
            description: 'Canonical plugin manifest.',
          },
        ],
      },
    });

    const parsed = await parseBrowserBundle(
      appendPublicationRecordCollection(WASM_HEADER, recordCollectionBytes),
    );

    expect(parsed.manifest).toMatchObject({
      pluginId: 'com.space-data-network.live-demo',
      version: '0.1.0',
    });
    expect(parsed.canonicalModuleHash).toEqual(canonicalHash);
    expect(parsed.canonicalModuleHashHex).toBe(
      '0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20',
    );
  });

  it('falls back to the first candidate that actually contains an MBL-backed REC trailer', async () => {
    const manifestBytes = encodePluginManifest({
      pluginId: 'com.space-data-network.live-demo',
      name: 'Live Demo',
      version: '0.1.0',
      invokeSurfaces: ['direct'],
      runtimeTargets: ['browser'],
    });
    const recordCollectionBytes = encodePublicationRecordCollection({
      mbl: {
        entries: [
          {
            entryId: 'manifest',
            role: 'manifest',
            sectionName: 'sds.manifest',
            payloadEncoding: 'flatbuffer',
            typeRef: {
              schemaName: 'PluginManifest.fbs',
              fileIdentifier: 'PMAN',
            },
            payload: manifestBytes,
            description: 'Canonical plugin manifest.',
          },
        ],
      },
    });

    const parsed = await parseFirstBrowserBundle([
      WASM_HEADER,
      appendPublicationRecordCollection(WASM_HEADER, recordCollectionBytes),
    ]);

    expect(parsed.manifest).toMatchObject({
      pluginId: 'com.space-data-network.live-demo',
      version: '0.1.0',
    });
  });
});
