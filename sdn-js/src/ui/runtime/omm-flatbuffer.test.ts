import { describe, expect, it } from 'vitest';

import { decodeOmmFlatBuffer } from './omm-flatbuffer';

const STARLINK_6292_OMM_BASE64 = 'HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA';

describe('decodeOmmFlatBuffer', () => {
  it('decodes CelesTrak OMM rows into SDS field names', () => {
    const decoded = decodeOmmFlatBuffer(Buffer.from(STARLINK_6292_OMM_BASE64, 'base64'));

    expect(decoded).toMatchObject({
      OBJECT_NAME: 'STARLINK-6292',
      OBJECT_ID: '2023-078J',
      NORAD_CAT_ID: 56775,
      EPOCH: '2026-05-10T10:45:31Z',
      CENTER_NAME: 'EARTH',
      ORIGINATOR: 'SDN-TEST',
      TIME_SYSTEM: 'UTC',
      MEAN_ELEMENT_THEORY: 'SGP4',
      EPHEMERIS_TYPE: 'SGP4',
      MEAN_MOTION: 14.98327971,
      ECCENTRICITY: 0.0003689,
      INCLINATION: 69.9989,
    });
    expect(decoded).not.toHaveProperty('GM');
    expect(decoded).not.toHaveProperty('MASS');
  });
});
