import assert from 'node:assert/strict';
import { describe, it } from 'vitest';
import * as flatbuffers from 'flatbuffers';
import { QRP } from 'spacedatastandards.org/lib/js/QRP/QRP.js';
import { QRPColumn } from 'spacedatastandards.org/lib/js/QRP/QRPColumn.js';
import { QRPRow } from 'spacedatastandards.org/lib/js/QRP/QRPRow.js';
import { QRPValue } from 'spacedatastandards.org/lib/js/QRP/QRPValue.js';
import { qrpKind } from 'spacedatastandards.org/lib/js/QRP/qrpKind.js';
import { qrpStatus } from 'spacedatastandards.org/lib/js/QRP/qrpStatus.js';
import { qrpValueKind } from 'spacedatastandards.org/lib/js/QRP/qrpValueKind.js';
import { decodeFlatSqlSegmentRows, FLATSQL_SYNC_SEGMENT_COLUMNS } from './flatsql-sync';

type Cell = number | bigint | string | null;
type Column = readonly [string, string];

function page(columns: readonly Column[], rows: Record<string, Cell>[]): QRP {
  const b = new flatbuffers.Builder(1024);
  const columnOffsets = columns.map(([name, type]) => {
    const n = b.createString(name), t = b.createString(type);
    QRPColumn.startQRPColumn(b);
    QRPColumn.addName(b, n);
    QRPColumn.addType(b, t);
    return QRPColumn.endQRPColumn(b);
  });
  const rowOffsets = rows.map((row) => {
    const offsets = columns.map(([name]) => {
      const value = row[name] ?? null;
      const text = typeof value === 'string' ? b.createString(value) : 0;
      QRPValue.startQRPValue(b);
      if (typeof value === 'string') {
        QRPValue.addKind(b, qrpValueKind.Text);
        QRPValue.addTextValue(b, text);
      } else if (value != null) {
        QRPValue.addKind(b, qrpValueKind.Int);
        QRPValue.addIntValue(b, BigInt(value));
      }
      return QRPValue.endQRPValue(b);
    });
    const cells = QRPRow.createCellsVector(b, offsets);
    QRPRow.startQRPRow(b);
    QRPRow.addCells(b, cells);
    return QRPRow.endQRPRow(b);
  });
  const c = QRP.createColumnsVector(b, columnOffsets), r = QRP.createRowsVector(b, rowOffsets);
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Page);
  QRP.addStatus(b, qrpStatus.Ok);
  QRP.addColumns(b, c);
  QRP.addRows(b, r);
  const root = QRP.endQRP(b);
  QRP.finishSizePrefixedQRPBuffer(b, root);
  return QRP.getSizePrefixedRootAsQRP(new flatbuffers.ByteBuffer(b.asUint8Array()));
}

describe('typed snapshot listing cutoffs', () => {
  const rows: Record<string, Cell>[] = [
    { SCHEMA_NAME: 'CAT.fbs', OFFSET: 8, PUBLICATION_CUTOFF: 33n, PUBLICATION_SOURCE_CUTOFF: 7n },
    { SCHEMA_NAME: 'CAT.fbs', BATCH_ID: 'ordinary', OFFSET: 16 },
    { SCHEMA_NAME: 'OCM.fbs', OFFSET: 0, PUBLICATION_CUTOFF: 0n, PUBLICATION_SOURCE_CUTOFF: 0n },
  ];

  it('appends the columns and preserves typed values, Null and explicit zero', () => {
    assert.deepEqual(FLATSQL_SYNC_SEGMENT_COLUMNS.slice(-2), [
      ['PUBLICATION_CUTOFF', 'int64'], ['PUBLICATION_SOURCE_CUTOFF', 'int64'],
    ]);
    const decoded = decodeFlatSqlSegmentRows(page(FLATSQL_SYNC_SEGMENT_COLUMNS, rows));
    assert.deepEqual(decoded.map((row) => [row.publicationCutoff, row.publicationSourceCutoff]), [
      [33, 7], [undefined, undefined], [0, 0],
    ]);
    assert.deepEqual(decoded.map((row) => [row.batchId, row.offset]), [['', 8], ['ordinary', 16], ['', 0]]);
  });

  it('reads every old-shaped row and ignores reordered unknown columns', () => {
    const old = FLATSQL_SYNC_SEGMENT_COLUMNS.slice(0, -2);
    const oldRows = decodeFlatSqlSegmentRows(page(old, rows));
    assert.equal(oldRows.length, rows.length);
    assert.ok(oldRows.every((row) => row.publicationCutoff === undefined && row.publicationSourceCutoff === undefined));
    const reordered: Column[] = [['UNKNOWN_COLUMN', 'string'], ...[...FLATSQL_SYNC_SEGMENT_COLUMNS].reverse()];
    assert.deepEqual(
      decodeFlatSqlSegmentRows(page(reordered, rows.map((row) => ({ ...row, UNKNOWN_COLUMN: 'ignored' })))),
      decodeFlatSqlSegmentRows(page(FLATSQL_SYNC_SEGMENT_COLUMNS, rows)),
    );
    assert.deepEqual(oldRows.map((row) => [row.schemaName, row.batchId, row.offset]), [
      ['CAT.fbs', '', 8], ['CAT.fbs', 'ordinary', 16], ['OCM.fbs', '', 0],
    ]);
  });

  it('rejects incorrectly typed, negative and unsafe numeric cutoffs', () => {
    for (const value of ['33', -1, 9007199254740993n]) {
      assert.throws(() => decodeFlatSqlSegmentRows(page(FLATSQL_SYNC_SEGMENT_COLUMNS, [{ PUBLICATION_CUTOFF: value }])), /cutoff|not an integer/);
    }
  });
});
