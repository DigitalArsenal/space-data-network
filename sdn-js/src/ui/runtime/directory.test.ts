import { describe, expect, it } from 'vitest';

import { normalizeDirectoryImportRequest, normalizeDirectoryRecord } from './directory';

describe('normalizeDirectoryRecord', () => {
  it('preserves rich EPM JSON payloads for directory detail views', () => {
    const record = normalizeDirectoryRecord({
      kind: 'node',
      peer_id: '16Uiu2HAmDirectoryDetail',
      dn: 'Directory Detail Node',
      epm_json: JSON.stringify({
        directory_kind: 'node',
        peer_id: '16Uiu2HAmDirectoryDetail',
        dn: 'Directory Detail Node',
        photo_data_url: 'data:image/png;base64,iVBORw0KGgo=',
        signature: 'abcdef',
      }),
    }, 'node');

    expect(record.epm_json).toContain('photo_data_url');
    expect(record.epm_json).toContain('signature');
  });
});

describe('normalizeDirectoryImportRequest', () => {
  it('preserves embedded binary EPM payloads from imported vCards', () => {
    const imported = normalizeDirectoryImportRequest({
      vcard: [
        'BEGIN:VCARD',
        'VERSION:3.0',
        'FN:Imported Node',
        'X-SDN-DIRECTORY-KIND:node',
        'X-SDN-PEER-ID:16Uiu2HAmImportedNode',
        'X-SDN-EPM-CID:bafkreimported',
        'X-SDN-EPM-B64:QUJDRA==',
        'END:VCARD',
      ].join('\r\n'),
    });

    expect(imported.imported).toBe(1);
    expect(imported.nodes[0]?.epm_json).toEqual({
      epm_base64: 'QUJDRA==',
    });
  });
});
