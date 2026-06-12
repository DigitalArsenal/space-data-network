import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Mirrors sdn-server/internal/update/carrier.go. The carrier is an inert
// storage envelope: a minimal valid WASM module whose only content is one
// custom section carrying the compressed update bundle. It is never
// instantiated or executed.
export const BUNDLE_SECTION_NAME = 'sdn.update.bundle.v1';

const WASM_MAGIC = Buffer.from([0x00, 0x61, 0x73, 0x6d]);
const WASM_VERSION = Buffer.from([0x01, 0x00, 0x00, 0x00]);
const CUSTOM_SECTION_ID = 0x00;

export function buildCarrier(bundleBytes) {
  const bundle = toBuffer(bundleBytes, 'bundleBytes');
  const name = Buffer.from(BUNDLE_SECTION_NAME, 'utf8');
  const payload = Buffer.concat([encodeUnsignedLeb128(name.length), name, bundle]);
  return Buffer.concat([
    WASM_MAGIC,
    WASM_VERSION,
    Buffer.from([CUSTOM_SECTION_ID]),
    encodeUnsignedLeb128(payload.length),
    payload,
  ]);
}

export function extractBundleBytes(wasmBytes) {
  const wasm = toBuffer(wasmBytes, 'wasmBytes');
  if (wasm.length < 8 || !wasm.subarray(0, 4).equals(WASM_MAGIC)) {
    throw new Error('update carrier is not a wasm module');
  }
  let offset = 8;
  while (offset < wasm.length) {
    const sectionId = wasm[offset];
    offset += 1;
    const sectionSize = readUnsignedLeb128(wasm, offset, 'malformed update carrier: invalid section size');
    offset += sectionSize.length;
    if (offset + sectionSize.value > wasm.length) {
      throw new Error('malformed update carrier: truncated section');
    }
    const payload = wasm.subarray(offset, offset + sectionSize.value);
    offset += sectionSize.value;
    if (sectionId !== CUSTOM_SECTION_ID) {
      continue;
    }
    const nameLength = readUnsignedLeb128(payload, 0, 'malformed update carrier: invalid section name length');
    if (nameLength.length + nameLength.value > payload.length) {
      throw new Error('malformed update carrier: truncated section name');
    }
    const name = payload.subarray(nameLength.length, nameLength.length + nameLength.value).toString('utf8');
    if (name === BUNDLE_SECTION_NAME) {
      return Buffer.from(payload.subarray(nameLength.length + nameLength.value));
    }
  }
  throw new Error('update carrier does not contain an SDN bundle section');
}

function encodeUnsignedLeb128(value) {
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new Error('LEB128 value must be a non-negative safe integer');
  }
  const bytes = [];
  let remaining = value;
  do {
    let byte = remaining % 0x80;
    remaining = Math.floor(remaining / 0x80);
    if (remaining > 0) {
      byte |= 0x80;
    }
    bytes.push(byte);
  } while (remaining > 0);
  return Buffer.from(bytes);
}

function readUnsignedLeb128(bytes, offset, message) {
  let value = 0;
  let shift = 0;
  for (let index = 0; index < 5; index += 1) {
    if (offset + index >= bytes.length) {
      throw new Error(message);
    }
    const byte = bytes[offset + index];
    value += (byte & 0x7f) * 2 ** shift;
    if ((byte & 0x80) === 0) {
      if (value > 0xffffffff) {
        throw new Error(message);
      }
      return { value, length: index + 1 };
    }
    shift += 7;
  }
  throw new Error(message);
}

function toBuffer(value, name) {
  if (Buffer.isBuffer(value)) {
    return value;
  }
  if (value instanceof Uint8Array) {
    return Buffer.from(value.buffer, value.byteOffset, value.byteLength);
  }
  if (value instanceof ArrayBuffer) {
    return Buffer.from(value);
  }
  throw new Error(`${name} must be a Buffer, Uint8Array, or ArrayBuffer`);
}

function required(value, name) {
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key.startsWith('--') || !value) {
      throw new Error(`Invalid argument near ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const options = parseArgs(process.argv.slice(2));
  const bundlePath = resolve(required(options.bundle, '--bundle'));
  const outPath = resolve(required(options.out, '--out'));
  const carrier = buildCarrier(await readFile(bundlePath));
  await mkdir(dirname(outPath), { recursive: true });
  await writeFile(outPath, carrier);
  console.log(outPath);
}
