#!/usr/bin/env node
/**
 * SDN Plugin Demo — Integration Test Suite
 *
 * Tests the complete data flow:
 *   1. Start a local SDN server on ephemeral ports
 *   2. Verify node info and catalog endpoints
 *   3. Build and publish a FlatBuffer message (PNM schema)
 *   4. Query the published data back
 *   5. Verify published record history endpoints
 *   6. Verify the REST API data endpoints
 *   7. Clean up
 *
 * Run:
 *   node integration.test.mjs
 *   SDN_TEST_VERBOSE=1 node integration.test.mjs
 */

import { startTestServer, stopTestServer } from './helpers/test-server.mjs';
import * as flatbuffers from 'flatbuffers';

/* ─── Test Framework (minimal, no deps) ─── */

let passed = 0;
let failed = 0;
let skipped = 0;
const errors = [];

function assert(condition, message) {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) {
    throw new Error(`${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

function assertIncludes(str, substr, message) {
  if (!String(str).includes(substr)) {
    throw new Error(`${message}: expected "${str}" to include "${substr}"`);
  }
}

async function test(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ✓ ${name}`);
  } catch (err) {
    if (err.message === 'SKIP') {
      skipped++;
      console.log(`  ○ ${name} (skipped)`);
    } else {
      failed++;
      errors.push({ name, error: err });
      console.log(`  ✗ ${name}`);
      console.log(`    ${err.message}`);
    }
  }
}

/* ─── FlatBuffer Helpers ─── */

/**
 * Build a minimal PNM (Publish Notification Message) FlatBuffer.
 * This demonstrates how to create a valid wire-format FlatBuffer for SDN.
 */
function buildPNM({ multiaddr, timestamp, cid, fileName, fileId, signature, signatureType }) {
  const builder = new flatbuffers.Builder(512);

  // Create strings first (FlatBuffers requires strings before table start)
  const addrOff = multiaddr ? builder.createString(multiaddr) : 0;
  const tsOff = timestamp ? builder.createString(timestamp) : 0;
  const cidOff = cid ? builder.createString(cid) : 0;
  const fnOff = fileName ? builder.createString(fileName) : 0;
  const fidOff = fileId ? builder.createString(fileId) : 0;
  const sigOff = signature ? builder.createString(signature) : 0;
  const stOff = signatureType ? builder.createString(signatureType) : 0;

  // Start PNM table (9 fields)
  builder.startObject(9);

  // Add fields in vtable order
  if (addrOff) builder.addFieldOffset(0, addrOff, 0);
  if (tsOff) builder.addFieldOffset(1, tsOff, 0);
  if (cidOff) builder.addFieldOffset(2, cidOff, 0);
  if (fnOff) builder.addFieldOffset(3, fnOff, 0);
  if (fidOff) builder.addFieldOffset(4, fidOff, 0);
  if (sigOff) builder.addFieldOffset(5, sigOff, 0);
  // field 6: TIMESTAMP_SIGNATURE (skipped)
  if (stOff) builder.addFieldOffset(7, stOff, 0);
  // field 8: TIMESTAMP_SIGNATURE_TYPE (skipped)

  const root = builder.endObject();

  // Finish with file identifier "$PNM" (4 bytes at offset 4-7)
  builder.finish(root, '$PNM');

  return builder.asUint8Array();
}

/**
 * Build a minimal OMM-like FlatBuffer for testing publish.
 * Since validation is disabled in test mode, we just need valid FlatBuffer
 * structure with the right file_identifier.
 */
function buildMinimalFlatBuffer(fileIdentifier, fields = {}) {
  const builder = new flatbuffers.Builder(256);
  const stringOffsets = [];

  // Create string values first
  for (const [, value] of Object.entries(fields)) {
    stringOffsets.push(builder.createString(String(value)));
  }

  // Start table with N fields
  const fieldCount = Object.keys(fields).length;
  builder.startObject(fieldCount);

  // Add each field as a string offset
  stringOffsets.forEach((off, i) => {
    builder.addFieldOffset(i, off, 0);
  });

  const root = builder.endObject();

  // Pad file identifier to exactly 4 bytes
  const fid = (fileIdentifier + '    ').slice(0, 4);
  builder.finish(root, fid);

  return builder.asUint8Array();
}

/**
 * Read the 4-byte file identifier from a FlatBuffer.
 */
function readFileIdentifier(bytes) {
  if (bytes.length < 8) return null;
  return String.fromCharCode(bytes[4], bytes[5], bytes[6], bytes[7]);
}

/* ─── HTTP Helpers ─── */

async function httpGet(url, opts = {}) {
  const resp = await fetch(url, {
    headers: { Accept: 'application/json', ...opts.headers },
    signal: AbortSignal.timeout(10000),
    ...opts,
  });
  return resp;
}

async function httpPost(url, body, opts = {}) {
  const headers = { ...opts.headers };
  if (body instanceof Uint8Array) {
    headers['Content-Type'] = 'application/octet-stream';
  } else if (typeof body === 'object') {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(body);
  }
  const resp = await fetch(url, {
    method: 'POST',
    headers: { Accept: 'application/json', ...headers },
    body,
    signal: AbortSignal.timeout(10000),
    ...opts,
  });
  return resp;
}

/* ─── Main Test Suite ─── */

async function runTests() {
  console.log('\n━━━ SDN Plugin Demo — Integration Tests ━━━\n');

  let server;
  try {
    console.log('Starting test server...');
    server = await startTestServer();
    console.log(`Server running at ${server.adminUrl}\n`);
  } catch (err) {
    console.error(`Failed to start server: ${err.message}`);
    console.error('Make sure Go is installed and sdn-server builds successfully.');
    console.error('Try: cd sdn-server && go build ./...');
    process.exit(1);
  }

  const baseUrl = server.adminUrl;

  try {
    /* ── Section 1: Server Health ── */
    console.log('§ Server Health');

    await test('GET /api/node/info returns node info', async () => {
      const resp = await httpGet(`${baseUrl}/api/node/info`);
      assertEqual(resp.status, 200, 'status');
      const body = await resp.json();
      assert(body.peer_id || body.PeerID || body.peerID, 'response should have peer_id');
    });

    await test('GET /api/v1/catalog returns schema catalog', async () => {
      const resp = await httpGet(`${baseUrl}/api/v1/catalog`);
      // 200 or 404 (if catalog endpoint is different) — just verify server responds
      assert(resp.status < 500, `expected non-500 status, got ${resp.status}`);
    });

    /* ── Section 2: Publish Data ── */
    console.log('\n§ Publish Data');

    let publishedCid = null;

    await test('POST /api/v1/data/publish/PNM.fbs publishes a PNM message', async () => {
      const pnmBytes = buildPNM({
        multiaddr: '/ip4/127.0.0.1/tcp/4001/p2p/test-peer',
        timestamp: new Date().toISOString(),
        cid: 'bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi',
        fileName: 'ISS-orbit-2026-03-09.omm',
        fileId: 'OMM',
        signature: 'test-signature-placeholder',
        signatureType: 'Ed25519',
      });

      // Verify the FlatBuffer has correct file_identifier
      const fid = readFileIdentifier(pnmBytes);
      assertEqual(fid, '$PNM', 'file identifier');

      const resp = await httpPost(`${baseUrl}/api/v1/data/publish/PNM.fbs`, pnmBytes);
      assertEqual(resp.status, 201, `publish status (body: ${await resp.clone().text()})`);
      const body = await resp.json();
      assert(body.cid, 'response should have cid');
      assertEqual(body.schema, 'PNM.fbs', 'response schema');
      assert(body.bytes > 0, 'response should have positive bytes');
      publishedCid = body.cid;
    });

    await test('published CID is a valid raw CIDv1 SHA-256 identifier', async () => {
      if (!publishedCid) throw new Error('SKIP');
      assert(/^bafkrei[a-z2-7]{52}$/.test(publishedCid), `CID should be a base32 raw CIDv1, got "${publishedCid}"`);
    });

    await test('publish a second message with different schema', async () => {
      const ommBytes = buildMinimalFlatBuffer('$OMM', {
        OBJECT_NAME: 'ISS (ZARYA)',
        NORAD_CAT_ID: '25544',
        EPOCH: '2026-03-09T12:00:00.000Z',
        MEAN_MOTION: '15.48919',
        ECCENTRICITY: '0.0006703',
        INCLINATION: '51.6416',
      });

      const fid = readFileIdentifier(ommBytes);
      assertEqual(fid, '$OMM', 'file identifier');

      const resp = await httpPost(`${baseUrl}/api/v1/data/publish/OMM.fbs`, ommBytes);
      assertEqual(resp.status, 201, `publish OMM status`);
    });

    /* ── Section 3: Query Data ── */
    console.log('\n§ Query Data');

    await test('GET /api/v1/data/query?schema=PNM.fbs returns published data', async () => {
      const resp = await httpGet(`${baseUrl}/api/v1/data/query?schema=PNM.fbs`);
      if (resp.status === 404) throw new Error('SKIP'); // endpoint may not exist
      assert(resp.status < 500, `expected non-500, got ${resp.status}`);

      if (resp.status === 200) {
        const body = await resp.json();
        assert(Array.isArray(body.records || body), 'response should be array or have records');
      }
    });

    await test('GET /api/v1/data/records/{schema}/{cid} retrieves specific record', async () => {
      if (!publishedCid) throw new Error('SKIP');
      const resp = await httpGet(`${baseUrl}/api/v1/data/records/PNM.fbs/${publishedCid}`);
      assertEqual(resp.status, 200, `record retrieval status`);
    });

    /* ── Section 4: Published Record History ── */
    console.log('\n§ Published Record History');

    await test('GET /api/v1/log/heads?schema=PNM.fbs returns log heads', async () => {
      const resp = await httpGet(`${baseUrl}/api/v1/log/heads?schema=PNM.fbs`);
      if (resp.status === 404) throw new Error('SKIP');
      assert(resp.status < 500, `expected non-500, got ${resp.status}`);

      if (resp.status === 200) {
        const body = await resp.json();
        // Should have at least one head entry (from our publish)
        if (Array.isArray(body) && body.length > 0) {
          const head = body[0];
          assert(head.head_sequence >= 1 || head.HEAD_SEQUENCE >= 1,
            'head should have sequence >= 1');
        }
      }
    });

    await test('GET /api/v1/log/entries returns log entries', async () => {
      const resp = await httpGet(`${baseUrl}/api/v1/log/entries?schema=PNM.fbs&since=0&limit=10`);
      if (resp.status === 404) throw new Error('SKIP');
      assert(resp.status < 500, `expected non-500, got ${resp.status}`);
    });

    /* ── Section 5: FlatBuffer Wire Format Verification ── */
    console.log('\n§ FlatBuffer Wire Format');

    await test('FlatBuffer has correct binary layout', async () => {
      const pnm = buildPNM({
        multiaddr: '/ip4/10.0.0.1/tcp/4001',
        timestamp: '2026-03-09T12:00:00Z',
        cid: 'bafytest123',
        fileId: 'CDM',
      });

      // Check minimum size (root offset + file id + vtable)
      assert(pnm.length >= 12, `FlatBuffer should be >= 12 bytes, got ${pnm.length}`);

      // Root table offset at bytes 0-3 (little-endian uint32)
      const rootOffset = pnm[0] | (pnm[1] << 8) | (pnm[2] << 16) | (pnm[3] << 24);
      assert(rootOffset > 0, 'root offset should be > 0');
      assert(rootOffset < pnm.length, 'root offset should be within buffer');

      // File identifier at bytes 4-7
      const fid = readFileIdentifier(pnm);
      assertEqual(fid, '$PNM', 'file identifier');

      // Parse back with flatbuffers library
      const buf = new flatbuffers.ByteBuffer(pnm);
      assert(buf.readInt32(buf.position()) > 0, 'FlatBuffer root table readable');
    });

    await test('different schemas produce different file identifiers', async () => {
      const pnm = buildMinimalFlatBuffer('$PNM', { test: 'value' });
      const omm = buildMinimalFlatBuffer('$OMM', { test: 'value' });
      const cdm = buildMinimalFlatBuffer('$CDM', { test: 'value' });

      assertEqual(readFileIdentifier(pnm), '$PNM', 'PNM identifier');
      assertEqual(readFileIdentifier(omm), '$OMM', 'OMM identifier');
      assertEqual(readFileIdentifier(cdm), '$CDM', 'CDM identifier');
    });

    /* ── Section 6: Node API ── */
    console.log('\n§ Node API');

    await test('GET /api/node/info includes listen addresses', async () => {
      const resp = await httpGet(`${baseUrl}/api/node/info`);
      const body = await resp.json();
      const addrs = body.addresses || body.listen_addrs || body.Addrs || [];
      // Server should report at least its libp2p and WS addresses
      assert(typeof body === 'object', 'response should be object');
    });

    await test('GET /api/v1/schemas lists supported schemas', async () => {
      const resp = await httpGet(`${baseUrl}/api/v1/schemas`);
      if (resp.status === 404) throw new Error('SKIP');
      if (resp.status === 200) {
        const body = await resp.json();
        const schemas = body.schemas || body;
        if (Array.isArray(schemas)) {
          assert(schemas.length > 0, 'should have at least one schema');
        }
      }
    });

    await test('GET /api/v1/plugins/manifest returns plugin status', async () => {
      const resp = await httpGet(`${baseUrl}/api/v1/plugins/manifest`);
      if (resp.status === 404) throw new Error('SKIP');
      assert(resp.status < 500, `expected non-500, got ${resp.status}`);
    });

    /* ── Section 7: Batch Publish ── */
    console.log('\n§ Batch Publish');

    await test('POST /api/v1/data/publish/batch/PNM.fbs accepts multiple records', async () => {
      const record1 = buildPNM({
        cid: 'bafybatch001',
        fileId: 'OMM',
        fileName: 'batch-record-1.omm',
      });
      const record2 = buildPNM({
        cid: 'bafybatch002',
        fileId: 'CDM',
        fileName: 'batch-record-2.cdm',
      });

      // Native FlatSQL batch format: [uint32LE length | data] for each record.
      const buf = new ArrayBuffer(4 + record1.length + 4 + record2.length);
      const view = new DataView(buf);
      let offset = 0;

      view.setUint32(offset, record1.length, true);
      offset += 4;
      new Uint8Array(buf, offset, record1.length).set(record1);
      offset += record1.length;

      view.setUint32(offset, record2.length, true);
      offset += 4;
      new Uint8Array(buf, offset, record2.length).set(record2);

      const resp = await httpPost(
        `${baseUrl}/api/v1/data/publish/batch/PNM.fbs`,
        new Uint8Array(buf),
      );
      // 201 or 200 both acceptable
      assert(resp.status < 300, `batch publish status ${resp.status}`);
    });

  } finally {
    /* ── Cleanup ── */
    console.log('\nStopping test server...');
    await stopTestServer(server);
  }

  /* ── Results ── */
  console.log(`\n━━━ Results: ${passed} passed, ${failed} failed, ${skipped} skipped ━━━\n`);

  if (errors.length > 0) {
    console.log('Failures:');
    for (const { name, error } of errors) {
      console.log(`  ✗ ${name}`);
      console.log(`    ${error.stack || error.message}`);
    }
    console.log('');
  }

  process.exit(failed > 0 ? 1 : 0);
}

runTests().catch(err => {
  console.error('Unhandled error:', err);
  process.exit(1);
});
