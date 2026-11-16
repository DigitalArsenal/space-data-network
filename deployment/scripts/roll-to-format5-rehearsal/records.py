"""Compare every scanned record's content address and exact stored bytes.

Raw evidence stays in size-prefixed FlatBuffer streams. The SQLite index holds
only digests and stream offsets. Public results contain per-standard totals.
"""
import hashlib
import json
from pathlib import Path
import sqlite3
import shutil
import tempfile
import struct
import urllib.request

API = 'http://127.0.0.1:5001/api/v1/data/scan'
MEDIA = 'application/vnd.sdn.flatbuffers.stream'


def scalar(data, offset, fmt):
    return struct.unpack_from('<' + fmt, data, offset)[0]


def field(data, slot):
    table = scalar(data, 0, 'I')
    vtable = table - scalar(data, table, 'i')
    entry = 4 + slot * 2
    if entry >= scalar(data, vtable, 'H'):
        return 0
    offset = scalar(data, vtable + entry, 'H')
    return table + offset if offset else 0


def number(data, slot, fmt):
    offset = field(data, slot)
    return scalar(data, offset, fmt) if offset else 0


def string(data, slot):
    offset = field(data, slot)
    if not offset:
        return b''
    start = offset + scalar(data, offset, 'I')
    size = scalar(data, start, 'I')
    return data[start + 4:start + 4 + size]


def request_frame(schema, cursor):
    # QRP slots: KIND=0 (Request, the default), SCHEMA_NAME=5,
    # LIMIT=20, CURSOR=21. A forward-built FlatBuffer needs no dependency.
    vtable = 8
    table = 56
    data = bytearray(72)
    struct.pack_into('<I4s', data, 0, table, b'$QRP')
    struct.pack_into('<HH', data, vtable, 48, 16)
    struct.pack_into('<i', data, table, table - vtable)
    for slot, position in ((5, 4), (20, 8), (21, 12)):
        struct.pack_into('<H', data, vtable + 4 + slot * 2, position)
    struct.pack_into('<I', data, table + 8, 50000)
    for position, value in ((4, schema), (12, cursor)):
        encoded = value.encode()
        data.extend(b'\0' * (-len(data) % 4))
        struct.pack_into('<I', data, table + position, len(data) - table - position)
        data.extend(struct.pack('<I', len(encoded)) + encoded + b'\0')
    return struct.pack('<I', len(data)) + data


def read_frame(response):
    prefix = response.read(4)
    if not prefix:
        return None
    if len(prefix) != 4:
        raise RuntimeError('truncated stream prefix')
    size = scalar(prefix, 0, 'I')
    data = response.read(size)
    if len(data) != size:
        raise RuntimeError('truncated record frame')
    return data


def scan(schema):
    cursor = ''
    scanned = 0
    total = None
    while True:
        request = urllib.request.Request(API, request_frame(schema, cursor),
                                         {'Content-Type': MEDIA})
        next_cursor = None
        with urllib.request.urlopen(request, timeout=120) as response:
            while True:
                header = read_frame(response)
                if header is None:
                    break
                if header[4:8] != b'$QRP' or number(header, 0, 'b') != 1:
                    raise RuntimeError('scan did not return a Page header')
                page_total = number(header, 24, 'Q')
                if total is None:
                    total = page_total
                if total != page_total:
                    raise RuntimeError('scan snapshot total changed')
                following = string(header, 22).decode()
                if next_cursor is not None and following != next_cursor:
                    raise RuntimeError('scan headers disagree on continuation')
                next_cursor = following
                for _ in range(number(header, 23, 'I')):
                    record = read_frame(response)
                    if record is None:
                        raise RuntimeError('missing record after scan header')
                    scanned += 1
                    yield record
        if next_cursor is None:
            raise RuntimeError('scan returned no header')
        if not next_cursor:
            if total != scanned:
                raise RuntimeError('scan total differs from records read')
            return
        if next_cursor == cursor:
            raise RuntimeError('scan cursor did not advance')
        cursor = next_cursor


def icn_fields(data):
    # The stored frame can itself be size-prefixed; preserve those bytes.
    if data[4:8] != b'$ICN' and data[8:12] == b'$ICN':
        data = data[4:]
    if data[4:8] != b'$ICN':
        raise RuntimeError('ICN schema returned another record type')
    return hashlib.sha256(string(data, 0)).digest(), number(data, 26, 'Q')


def snapshot(directory, schemas):
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=False)
    results = {}
    stats_comparison = {}
    for schema in sorted(schemas):
        if not schema.replace('_', '').replace('.fbs', '').isalnum() or not schema.endswith('.fbs'):
            raise ValueError('unexpected schema spelling')
        temporary = tempfile.TemporaryDirectory(prefix='record-snapshot-')
        index = Path(temporary.name) / 'index.sqlite'
        db = sqlite3.connect(index)
        db.execute('CREATE TABLE records (cid BLOB PRIMARY KEY, offset INTEGER, size INTEGER, occurrences INTEGER DEFAULT 1, seen INTEGER DEFAULT 0) WITHOUT ROWID')
        db.execute('CREATE TABLE connectors (id BLOB PRIMARY KEY) WITHOUT ROWID')
        count = 0
        with (directory / (schema + '.sds')).open('w+b') as stream:
            for data in scan(schema):
                cid = hashlib.sha256(data).digest()
                offset = stream.tell()
                prior = db.execute('INSERT INTO records(cid,offset,size) VALUES(?,?,?) '
                                   'ON CONFLICT(cid) DO UPDATE SET occurrences=occurrences+1 '
                                   'RETURNING offset,size,occurrences',
                                   (cid, offset, len(data))).fetchone()
                if prior[2] > 1:
                    stream.seek(prior[0] + 4)
                    if prior[1] != len(data) or stream.read(prior[1]) != data:
                        raise RuntimeError('one content address has different baseline bytes')
                    stream.seek(offset)
                stream.write(struct.pack('<I', len(data)) + data)
                if schema == 'ICN.fbs':
                    connector, _ = icn_fields(data)
                    db.execute('INSERT OR IGNORE INTO connectors VALUES(?)', (connector,))
                count += 1
                if count % 20000 == 0:
                    db.commit()
        db.commit()
        db.close()
        shutil.copy2(index, directory / (schema + '.sqlite'))
        temporary.cleanup()
        # Stats sums source associations; a raw scan enumerates content addresses.
        # The scan validates its own advertised total against all frames read.
        stats_comparison[schema] = {'stats_source_count': schemas[schema],
                                    'scanned_records': count}
        (directory / 'scan-versus-stats.json').write_text(json.dumps(stats_comparison, sort_keys=True) + '\n')
        results[schema] = count
    (directory / 'summary.json').write_text(json.dumps(results, sort_keys=True) + '\n')
    return results


def compare(directory, schemas, boot_ms, expected_boot_additions):
    directory = Path(directory)
    before = json.loads((directory / 'summary.json').read_text())
    results = {}
    for schema in sorted(set(schemas) | set(before)):
        if schema not in before:
            results[schema] = {'unexpected_standard': True}
            continue
        temporary = tempfile.TemporaryDirectory(prefix='record-compare-')
        index = Path(temporary.name) / 'index.sqlite'
        shutil.copy2(directory / (schema + '.sqlite'), index)
        db = sqlite3.connect(index)
        added = changed = unexplained = matched = 0
        added_connectors = set()
        with (directory / (schema + '.sds')).open('rb') as stream:
            for data in scan(schema):
                cid = hashlib.sha256(data).digest()
                row = db.execute('SELECT offset,size,seen,occurrences FROM records WHERE cid=?', (cid,)).fetchone()
                if row:
                    offset, size, seen, occurrences = row
                    stream.seek(offset + 4)
                    if size != len(data) or stream.read(size) != data:
                        changed += 1
                    if seen >= occurrences:
                        unexplained += 1
                    db.execute('UPDATE records SET seen=seen+1 WHERE cid=?', (cid,))
                    matched += 1
                else:
                    added += 1
                    if schema == 'ICN.fbs':
                        connector, updated = icn_fields(data)
                        known = db.execute('SELECT 1 FROM connectors WHERE id=?', (connector,)).fetchone()
                        if not known or updated < boot_ms or connector in added_connectors:
                            unexplained += 1
                        added_connectors.add(connector)
                    else:
                        unexplained += 1
                if (matched + added) % 20000 == 0:
                    db.commit()
        missing = db.execute('SELECT coalesce(sum(max(occurrences-seen,0)),0) FROM records').fetchone()[0]
        distinct_before = db.execute('SELECT count(*) FROM records').fetchone()[0]
        db.commit()
        db.close()
        temporary.cleanup()
        if schema == 'ICN.fbs' and added != expected_boot_additions:
            unexplained += abs(added - expected_boot_additions)
        results[schema] = dict(before=before[schema], distinct_before=distinct_before,
                               baseline_duplicate_occurrences=before[schema]-distinct_before,
                               matched=matched, added=added,
                               missing=missing, changed=changed, unexplained=unexplained)
    passed = all(not any(row.get(key, 0) for key in
                         ('missing', 'changed', 'unexplained', 'unexpected_standard'))
                 for row in results.values())
    return {'passed': passed, 'standards': results,
            'method': 'SHA-256 content address lookup followed by exact byte comparison'}
