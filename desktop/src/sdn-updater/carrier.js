// SDN update carrier parsing. The WASM carrier is a storage envelope only:
// the bundle bytes are read out of a custom section without ever compiling,
// instantiating, or executing the module. This is the Node twin of
// sdn-server/internal/update/carrier.go and must keep the section name and
// wire format identical.

const WASM_BUNDLE_SECTION_NAME = 'sdn.update.bundle.v1'
const WASM_MAGIC = Buffer.from([0x00, 0x61, 0x73, 0x6d])
const WASM_HEADER_LENGTH = 8
const CUSTOM_SECTION_ID = 0

function normalizeBytes (value) {
  if (Buffer.isBuffer(value)) {
    return value
  }
  if (value instanceof Uint8Array) {
    return Buffer.from(value.buffer, value.byteOffset, value.byteLength)
  }
  if (value instanceof ArrayBuffer) {
    return Buffer.from(value)
  }

  throw new Error('update carrier is not a wasm module')
}

// Reads an unsigned LEB128 varuint32 at offset. Returns { value, length }.
function readVaruint32 (bytes, offset) {
  let value = 0
  let length = 0

  while (true) {
    if (offset + length >= bytes.length || length >= 5) {
      throw new Error('malformed update carrier: invalid varuint32')
    }
    const byte = bytes[offset + length]
    value += (byte & 0x7f) * 2 ** (7 * length)
    length++
    if ((byte & 0x80) === 0) {
      break
    }
  }

  if (value > 0xffffffff) {
    throw new Error('malformed update carrier: invalid varuint32')
  }

  return { value, length }
}

// Walks the WASM module sections without instantiating the module and returns
// the payload bytes of the SDN bundle custom section.
function extractBundleBytes (wasmBytes) {
  const bytes = normalizeBytes(wasmBytes)

  if (bytes.length < WASM_HEADER_LENGTH || !bytes.subarray(0, 4).equals(WASM_MAGIC)) {
    throw new Error('update carrier is not a wasm module')
  }

  let offset = WASM_HEADER_LENGTH
  while (offset < bytes.length) {
    const sectionId = bytes[offset]
    offset++

    const sectionSize = readVaruint32(bytes, offset)
    offset += sectionSize.length

    if (offset + sectionSize.value > bytes.length) {
      throw new Error('malformed update carrier: truncated section')
    }
    const payload = bytes.subarray(offset, offset + sectionSize.value)
    offset += sectionSize.value

    if (sectionId !== CUSTOM_SECTION_ID) {
      continue
    }

    const nameLength = readVaruint32(payload, 0)
    if (nameLength.length + nameLength.value > payload.length) {
      throw new Error('malformed update carrier: truncated section name')
    }
    const name = payload.subarray(nameLength.length, nameLength.length + nameLength.value).toString('utf8')
    if (name === WASM_BUNDLE_SECTION_NAME) {
      return payload.subarray(nameLength.length + nameLength.value)
    }
  }

  throw new Error('update carrier does not contain an SDN bundle section')
}

module.exports = {
  WASM_BUNDLE_SECTION_NAME,
  extractBundleBytes
}
