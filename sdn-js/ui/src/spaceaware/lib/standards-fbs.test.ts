import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { describe, expect, it } from 'vitest';
import {
  VENDORED_FBS_BY_CODE,
  extractFbsHeaderVersion,
  fbsFieldTypeColor,
  findRootTableName,
  getVendoredFbsSource,
  getVendoredSchema,
  parseFbsFields,
  parseFbsSchema,
} from './standards-fbs';

const require = createRequire(import.meta.url);
// Independent read of the real vendored files (NOT through the glob under
// test) — this is what the byte-equality tests compare against, so a bug in
// the glob itself can't hide behind comparing the glob to itself.
function readVendoredFileDirect(code: string): string {
  return readFileSync(require.resolve(`spacedatastandards.org/schema/${code}/main.fbs`), 'utf8');
}

// ---------------------------------------------------------------------------
// VENDORED_FBS_BY_CODE / getVendoredFbsSource — the import.meta.glob wiring
// ---------------------------------------------------------------------------

describe('VENDORED_FBS_BY_CODE', () => {
  it('loads every vendored standard on this spacedatastandards.org pin (169 on v1.136.0)', () => {
    expect(VENDORED_FBS_BY_CODE.size).toBeGreaterThanOrEqual(169);
  });

  it('carries CAT and OMM (the two byte-equality codes this task requires)', () => {
    expect(VENDORED_FBS_BY_CODE.has('CAT')).toBe(true);
    expect(VENDORED_FBS_BY_CODE.has('OMM')).toBe(true);
  });

  it('matches the CAT file on disk byte-for-byte', () => {
    expect(VENDORED_FBS_BY_CODE.get('CAT')).toBe(readVendoredFileDirect('CAT'));
  });

  it('matches the OMM file on disk byte-for-byte', () => {
    expect(VENDORED_FBS_BY_CODE.get('OMM')).toBe(readVendoredFileDirect('OMM'));
  });
});

describe('getVendoredFbsSource', () => {
  it('returns the raw text for a known code', () => {
    expect(getVendoredFbsSource('CAT')).toContain('table CAT {');
  });

  it('returns null for a code with no vendored schema (never fabricates one)', () => {
    expect(getVendoredFbsSource('NOT-A-REAL-CODE')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// findRootTableName — table resolution edge cases
// ---------------------------------------------------------------------------

describe('findRootTableName', () => {
  it('picks the table matching the standard code exactly', () => {
    const source = `table CAT { OBJECT_NAME: string; }\ntable OtherWrapper { x: int; }\nroot_type CAT;`;
    expect(findRootTableName(source, 'CAT')).toBe('CAT');
  });

  it('falls back to the root_type declaration when no table matches the code', () => {
    const source = `table RFM { REFERENCE_FRAME: int; }\nroot_type RFM;`;
    expect(findRootTableName(source, 'DOES_NOT_MATCH')).toBe('RFM');
  });

  it('falls back to the first declared table when neither the code nor root_type resolves', () => {
    const source = `table FirstOne { a: int; }\ntable SecondOne { b: int; }`;
    expect(findRootTableName(source, 'NOPE')).toBe('FirstOne');
  });

  it('returns null when the source declares no table at all', () => {
    expect(findRootTableName('enum Foo : byte { A, B }', 'CAT')).toBeNull();
  });

  it('resolves the real CDM schema to the CDM table, not its nested CDMObject table', () => {
    const source = VENDORED_FBS_BY_CODE.get('CDM')!;
    expect(findRootTableName(source, 'CDM')).toBe('CDM');
  });
});

// ---------------------------------------------------------------------------
// parseFbsFields — the field/note extraction edge cases the loop task calls
// out explicitly (vectors, enums+defaults, attributes, nested tables,
// multi-line doc comments, plain // comments, no comment)
// ---------------------------------------------------------------------------

describe('parseFbsFields', () => {
  it('extracts a plain scalar field with a single-line doc comment', () => {
    const fields = parseFbsFields('/// International Designator\nOBJECT_ID: string;');
    expect(fields).toEqual([{ name: 'OBJECT_ID', type: 'string', note: 'International Designator' }]);
  });

  it('drops a default value from the type column (enum field)', () => {
    const fields = parseFbsFields('/// Object type\nOBJECT_TYPE: spaceObjectClass = UNKNOWN;');
    expect(fields).toEqual([{ name: 'OBJECT_TYPE', type: 'spaceObjectClass', note: 'Object type' }]);
  });

  it('drops a trailing attribute from the type column', () => {
    const fields = parseFbsFields('/// License key id\nLICENSE_ID: string (required);');
    expect(fields).toEqual([{ name: 'LICENSE_ID', type: 'string', note: 'License key id' }]);
  });

  it('keeps vector brackets in the type column', () => {
    const fields = parseFbsFields('/// Vector of payloads\nPAYLOADS: [PLD];\n/// Covariance\nCOVARIANCE:[double];');
    expect(fields).toEqual([
      { name: 'PAYLOADS', type: '[PLD]', note: 'Vector of payloads' },
      { name: 'COVARIANCE', type: '[double]', note: 'Covariance' },
    ]);
  });

  it('handles a nested/other-table-typed field with no space before the colon', () => {
    const fields = parseFbsFields('/// Point of Contact\nPOC:EPM;');
    expect(fields).toEqual([{ name: 'POC', type: 'EPM', note: 'Point of Contact' }]);
  });

  it('takes only the FIRST line of a multi-line doc comment, not the whole block', () => {
    const fields = parseFbsFields(
      [
        '/// Explicit key bytes when a module must receive them on a port.',
        '/// This may be field-encrypted using the SDS/da-flatbuffers header-first',
        '/// encryption flow when transported to a specific recipient.',
        'KEY_BYTES: [ubyte] (encrypted);',
      ].join('\n'),
    );
    expect(fields).toEqual([
      {
        name: 'KEY_BYTES',
        type: '[ubyte]',
        note: 'Explicit key bytes when a module must receive them on a port.',
      },
    ]);
  });

  it('ignores a plain // comment — it never becomes a note', () => {
    const fields = parseFbsFields(['/// A comment', 'COMMENT:string;', '// Catalog Definition', 'OBJECT:CAT;'].join('\n'));
    expect(fields).toEqual([
      { name: 'COMMENT', type: 'string', note: 'A comment' },
      { name: 'OBJECT', type: 'CAT', note: '' },
    ]);
  });

  it('renders an empty note for a field with no comment at all', () => {
    const fields = parseFbsFields('REFERENCE_FRAME: RFMUnion;\nINDEX: int;\nNAME: string;');
    expect(fields).toEqual([
      { name: 'REFERENCE_FRAME', type: 'RFMUnion', note: '' },
      { name: 'INDEX', type: 'int', note: '' },
      { name: 'NAME', type: 'string', note: '' },
    ]);
  });

  it('does not let a blank line between comment and field break the association', () => {
    const fields = parseFbsFields('/// Has a blank line after it\n\nOBJECT_ID: string;');
    expect(fields[0]).toEqual({ name: 'OBJECT_ID', type: 'string', note: 'Has a blank line after it' });
  });

  it('returns an empty list for an empty table body', () => {
    expect(parseFbsFields('')).toEqual([]);
    expect(parseFbsFields('   \n  \n')).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// extractFbsHeaderVersion
// ---------------------------------------------------------------------------

describe('extractFbsHeaderVersion', () => {
  it('extracts the per-file // Version: header line', () => {
    expect(extractFbsHeaderVersion('// Hash: abc\n// Version: 2.1.0\n// ---END---')).toBe('2.1.0');
  });

  it('matches the real CAT schema header version', () => {
    const source = VENDORED_FBS_BY_CODE.get('CAT')!;
    expect(extractFbsHeaderVersion(source)).toBe('1.0.2');
  });

  it('returns null when there is no Version header', () => {
    expect(extractFbsHeaderVersion('table X { a: int; }')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// parseFbsSchema — the full pipeline against real vendored files, including
// unions, enums (with multi-line per-member doc comments), includes, and
// nested tables (RFM, CDM)
// ---------------------------------------------------------------------------

describe('parseFbsSchema', () => {
  it('parses the real CAT schema: table name, label, version, and a known field', () => {
    const source = VENDORED_FBS_BY_CODE.get('CAT')!;
    const schema = parseFbsSchema(source, 'CAT')!;
    expect(schema.tableName).toBe('CAT');
    expect(schema.label).toBe('Catalog Entity Message');
    expect(schema.version).toBe('1.0.2');
    expect(schema.raw).toBe(source);
    const noradField = schema.fields.find((f) => f.name === 'NORAD_CAT_ID');
    expect(noradField).toEqual({ name: 'NORAD_CAT_ID', type: 'uint', note: 'NORAD Catalog Number' });
  });

  it('parses the real OMM schema and finds a vector-typed field', () => {
    const source = VENDORED_FBS_BY_CODE.get('OMM')!;
    const schema = parseFbsSchema(source, 'OMM')!;
    expect(schema.tableName).toBe('OMM');
    const covariance = schema.fields.find((f) => f.name === 'COVARIANCE');
    expect(covariance?.type).toBe('[double]');
  });

  it('parses the real EPM schema (nested Address table, [string]/[CryptoKey] vectors)', () => {
    const source = VENDORED_FBS_BY_CODE.get('EPM')!;
    const schema = parseFbsSchema(source, 'EPM')!;
    expect(schema.tableName).toBe('EPM');
    expect(schema.fields.find((f) => f.name === 'ADDRESS')?.type).toBe('Address');
    expect(schema.fields.find((f) => f.name === 'ALTERNATE_NAMES')?.type).toBe('[string]');
    expect(schema.fields.find((f) => f.name === 'KEYS')?.type).toBe('[CryptoKey]');
  });

  it('parses the real RFM schema (a union type + enums with multi-line per-member doc comments) without crashing', () => {
    const source = VENDORED_FBS_BY_CODE.get('RFM')!;
    const schema = parseFbsSchema(source, 'RFM')!;
    expect(schema.tableName).toBe('RFM');
    expect(schema.label).toBe('Reference Frame Message');
    expect(schema.fields).toEqual([
      { name: 'REFERENCE_FRAME', type: 'RFMUnion', note: '' },
      { name: 'INDEX', type: 'int', note: '' },
      { name: 'NAME', type: 'string', note: '' },
    ]);
  });

  it('parses the real CDM schema (includes + a nested CDMObject table) and resolves to the CDM root table, not CDMObject', () => {
    const source = VENDORED_FBS_BY_CODE.get('CDM')!;
    const schema = parseFbsSchema(source, 'CDM')!;
    expect(schema.tableName).toBe('CDM');
    expect(schema.fields.find((f) => f.name === 'OBJECT1')?.type).toBe('CDMObject');
    // The nested CDMObject table's own fields must not leak into CDM's list.
    expect(schema.fields.some((f) => f.name === 'OPERATOR_ORGANIZATION')).toBe(false);
  });

  it('parses the real PLK schema (attributes-after-type on every field)', () => {
    const source = VENDORED_FBS_BY_CODE.get('PLK')!;
    const schema = parseFbsSchema(source, 'PLK')!;
    expect(schema.fields.find((f) => f.name === 'LICENSE_ID')).toEqual({
      name: 'LICENSE_ID',
      type: 'string',
      note: 'Unique license key identifier',
    });
  });

  it('returns null for blank source', () => {
    expect(parseFbsSchema('', 'CAT')).toBeNull();
    expect(parseFbsSchema('   ', 'CAT')).toBeNull();
  });

  it('returns null when the source has no table at all', () => {
    expect(parseFbsSchema('enum Foo : byte { A }', 'CAT')).toBeNull();
  });

  it('falls back to the table name itself as the label when there is no leading doc comment', () => {
    const schema = parseFbsSchema('table PLAIN {\n  A: int;\n}\nroot_type PLAIN;', 'PLAIN')!;
    expect(schema.label).toBe('PLAIN');
  });
});

// ---------------------------------------------------------------------------
// getVendoredSchema — the cached per-code accessor
// ---------------------------------------------------------------------------

describe('getVendoredSchema', () => {
  it('resolves a real code end to end', () => {
    const schema = getVendoredSchema('CAT');
    expect(schema?.tableName).toBe('CAT');
  });

  it('returns the same field data on a second call (cache does not corrupt/refetch)', () => {
    const first = getVendoredSchema('OMM');
    const second = getVendoredSchema('OMM');
    expect(second).toEqual(first);
  });

  it('returns null for an unknown code, never throws', () => {
    expect(() => getVendoredSchema('ZZZ-UNKNOWN')).not.toThrow();
    expect(getVendoredSchema('ZZZ-UNKNOWN')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// fbsFieldTypeColor
// ---------------------------------------------------------------------------

describe('fbsFieldTypeColor', () => {
  it('colors a vector type purple', () => {
    expect(fbsFieldTypeColor('[double]')).toBe('#c77dff');
  });

  it('colors string green', () => {
    expect(fbsFieldTypeColor('string')).toBe('#5ad6a0');
  });

  it('colors an uppercase-first custom type amber', () => {
    expect(fbsFieldTypeColor('EPM')).toBe('#ffb24d');
  });

  it('colors a native scalar / lowercase-first enum type the neutral cyan', () => {
    expect(fbsFieldTypeColor('double')).toBe('#9fd4f5');
    expect(fbsFieldTypeColor('uint')).toBe('#9fd4f5');
    expect(fbsFieldTypeColor('spaceObjectClass')).toBe('#9fd4f5');
  });
});
