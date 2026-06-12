const { test, expect } = require('@playwright/test')
const { WASM_BUNDLE_SECTION_NAME, extractBundleBytes } = require('../../src/sdn-updater/carrier')

const WASM_HEADER = Buffer.from([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00])

function leb128 (value) {
  const bytes = []
  do {
    let byte = value & 0x7f
    value = Math.floor(value / 128)
    if (value > 0) {
      byte |= 0x80
    }
    bytes.push(byte)
  } while (value > 0)
  return Buffer.from(bytes)
}

function customSection (name, data) {
  const nameBytes = Buffer.from(name, 'utf8')
  const payload = Buffer.concat([leb128(nameBytes.length), nameBytes, data])
  return Buffer.concat([Buffer.from([0x00]), leb128(payload.length), payload])
}

function typeSection () {
  // section id 1 with an empty type vector
  return Buffer.from([0x01, 0x01, 0x00])
}

function wasmModule (...sections) {
  return Buffer.concat([WASM_HEADER, ...sections])
}

test.describe('SDN update carrier extraction', () => {
  test('round-trips bundle bytes through the SDN custom section without instantiation', () => {
    const bundleBytes = Buffer.from('compressed desktop bundle payload'.repeat(16))
    const wasmBytes = wasmModule(
      typeSection(),
      customSection('name', Buffer.from('decoy section')),
      customSection(WASM_BUNDLE_SECTION_NAME, bundleBytes),
      customSection('trailing', Buffer.from('after the bundle'))
    )

    const extracted = extractBundleBytes(wasmBytes)

    expect(Buffer.from(extracted).equals(bundleBytes)).toBe(true)
  })

  test('extracts a bundle large enough to need multi-byte LEB128 section sizes', () => {
    const bundleBytes = Buffer.alloc(70000, 0xab)
    const wasmBytes = wasmModule(customSection(WASM_BUNDLE_SECTION_NAME, bundleBytes))

    const extracted = extractBundleBytes(wasmBytes)

    expect(extracted.byteLength).toBe(bundleBytes.byteLength)
    expect(Buffer.from(extracted).equals(bundleBytes)).toBe(true)
  })

  test('accepts Uint8Array input', () => {
    const bundleBytes = Buffer.from('typed array carrier')
    const wasmBytes = wasmModule(customSection(WASM_BUNDLE_SECTION_NAME, bundleBytes))

    const extracted = extractBundleBytes(new Uint8Array(wasmBytes))

    expect(Buffer.from(extracted).equals(bundleBytes)).toBe(true)
  })

  test('rejects non-wasm input', () => {
    expect(() => extractBundleBytes(Buffer.from('not a wasm module')))
      .toThrow('update carrier is not a wasm module')
    expect(() => extractBundleBytes(Buffer.from([0x00, 0x61, 0x73])))
      .toThrow('update carrier is not a wasm module')
    expect(() => extractBundleBytes('strings are not carriers'))
      .toThrow('update carrier is not a wasm module')
  })

  test('rejects a wasm module without the SDN bundle section', () => {
    const wasmBytes = wasmModule(
      typeSection(),
      customSection('some.other.section', Buffer.from('not the bundle'))
    )

    expect(() => extractBundleBytes(wasmBytes))
      .toThrow('update carrier does not contain an SDN bundle section')
    expect(() => extractBundleBytes(WASM_HEADER))
      .toThrow('update carrier does not contain an SDN bundle section')
  })

  test('rejects truncated sections', () => {
    const section = customSection(WASM_BUNDLE_SECTION_NAME, Buffer.from('bundle'))
    const truncated = wasmModule(section).subarray(0, WASM_HEADER.length + section.length - 3)

    expect(() => extractBundleBytes(truncated))
      .toThrow('malformed update carrier: truncated section')
  })
})
