import * as flatbuffers from 'flatbuffers';
import { describe, expect, it } from 'vitest';

import { CAT } from 'spacedatastandards.org/lib/js/CAT/CAT.js';
import { operationalState } from 'spacedatastandards.org/lib/js/CAT/operationalState.js';
import { spaceObjectClass } from 'spacedatastandards.org/lib/js/CAT/spaceObjectClass.js';

import { decodeCatFlatBuffer } from './cat-flatbuffer';

describe('decodeCatFlatBuffer', () => {
  it('decodes CAT values without inventing unavailable physical fields', () => {
    const bytes = buildCatFixture({
      objectName: 'COSMOS 2251 DEB',
      objectId: '1993-036AAB',
      noradCatId: 33757,
      objectType: spaceObjectClass.DEBRIS,
      opsStatus: operationalState.DECAYED,
      rcs: 0.01,
      maneuverable: false,
    });

    const decoded = decodeCatFlatBuffer(bytes);

    expect(decoded).toMatchObject({
      OBJECT_NAME: 'COSMOS 2251 DEB',
      OBJECT_ID: '1993-036AAB',
      NORAD_CAT_ID: 33757,
      OBJECT_TYPE: 'DEBRIS',
      OPS_STATUS_CODE: 'DECAYED',
      RCS: 0.01,
    });
    expect(decoded).not.toHaveProperty('MANEUVERABLE');
    expect(decoded).not.toHaveProperty('MASS');
    expect(decoded).not.toHaveProperty('SIZE');
  });
});

function buildCatFixture(options: {
  objectName: string;
  objectId: string;
  noradCatId: number;
  objectType: spaceObjectClass;
  opsStatus: operationalState;
  rcs: number;
  maneuverable: boolean;
}): Uint8Array {
  const builder = new flatbuffers.Builder(256);
  const objectName = builder.createString(options.objectName);
  const objectId = builder.createString(options.objectId);

  CAT.startCAT(builder);
  CAT.addObjectName(builder, objectName);
  CAT.addObjectId(builder, objectId);
  CAT.addNoradCatId(builder, options.noradCatId);
  CAT.addObjectType(builder, options.objectType);
  CAT.addOpsStatusCode(builder, options.opsStatus);
  CAT.addRcs(builder, options.rcs);
  CAT.addManeuverable(builder, options.maneuverable);
  const cat = CAT.endCAT(builder);
  CAT.finishSizePrefixedCATBuffer(builder, cat);
  return builder.asUint8Array();
}
