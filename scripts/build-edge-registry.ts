#!/usr/bin/env npx ts-node
/**
 * Build Edge Registry - Generates encrypted WASM module with edge relay list
 *
 * This script:
 * 1. Reads relay list from JSON file
 * 2. Encrypts the list with XChaCha20
 * 3. Generates C++ source with embedded encrypted data
 * 4. Compiles to WASM using Emscripten
 */

import { execSync } from 'child_process';
import { writeFileSync, readFileSync, mkdirSync, existsSync } from 'fs';
import { randomBytes, createCipheriv } from 'crypto';
import { join } from 'path';

interface RelayInfo {
  peerId: string;
  multiaddr: string;
  lastSeen: number;
  region?: string;
}

const OUTPUT_DIR = join(__dirname, '..', 'sdn-js', 'wasm');
const SRC_DIR = join(__dirname, '..', 'sdn-js', 'src', 'wasm-src');

async function main() {
  const relaysFile = process.argv[2];

  if (!relaysFile) {
    console.log('Usage: build-edge-registry.ts <relays.json>');
    console.log('');
    console.log('Example:');
    console.log('  npx ts-node scripts/build-edge-registry.ts /tmp/edge-relays.json');
    process.exit(1);
  }

  // Read relay list
  const relaysData = readFileSync(relaysFile, 'utf-8');
  const relays: RelayInfo[] = JSON.parse(relaysData);

  console.log(`Building edge registry with ${relays.length} relays...`);

  await buildEdgeRegistry(relays);

  console.log('Done!');
}

async function buildEdgeRegistry(relays: RelayInfo[]) {
  // Ensure output directories exist
  if (!existsSync(OUTPUT_DIR)) {
    mkdirSync(OUTPUT_DIR, { recursive: true });
  }
  if (!existsSync(SRC_DIR)) {
    mkdirSync(SRC_DIR, { recursive: true });
  }

  // 1. Generate unique key for this build
  const key = randomBytes(32);
  const nonce = randomBytes(24);

  // 2. Create relay list JSON (just multiaddrs)
  const relayList = JSON.stringify(relays.map(r => r.multiaddr));

  // 3. Encrypt the relay list using XChaCha20-Poly1305
  // Note: Node.js crypto doesn't have XChaCha20, using ChaCha20-Poly1305 as approximation
  // In production, use a proper XChaCha20 implementation
  const cipher = createCipheriv('chacha20-poly1305', key, nonce.slice(0, 12), {
    authTagLength: 16,
  });
  const encrypted = Buffer.concat([
    cipher.update(relayList, 'utf8'),
    cipher.final(),
    cipher.getAuthTag(),
  ]);

  // 4. Obfuscate the key (XOR with random mask)
  const keyMask = randomBytes(32);
  const obfuscatedKey = Buffer.alloc(64);
  for (let i = 0; i < 32; i++) {
    obfuscatedKey[i] = key[i] ^ keyMask[i];
    obfuscatedKey[32 + i] = keyMask[i];
  }

  // 5. Generate C++ source with embedded data
  const cppSource = generateCppSource(nonce, encrypted, obfuscatedKey, relays.length);

  // Write C++ source
  const cppPath = join(SRC_DIR, 'edge-relays.cpp');
  writeFileSync(cppPath, cppSource);
  console.log(`Generated ${cppPath}`);

  // 6. Write header with embedded data
  const headerSource = generateHeaderSource(nonce, encrypted, obfuscatedKey);
  const headerPath = join(SRC_DIR, 'edge-relays-data.h');
  writeFileSync(headerPath, headerSource);
  console.log(`Generated ${headerPath}`);

  // 7. Compile to WASM using Emscripten
  try {
    compileToWasm(cppPath);
  } catch (err) {
    console.warn('WASM compilation skipped (Emscripten not installed):', err);
    console.log('To compile, install Emscripten and run:');
    console.log(`  emcc ${cppPath} -o ${join(OUTPUT_DIR, 'edge-relays.wasm')} \\`);
    console.log('    -s EXPORTED_FUNCTIONS="[_get_edge_relays, _get_relay_count]" \\');
    console.log('    -s EXPORTED_RUNTIME_METHODS="[UTF8ToString]" -O3');
  }

  // 8. Generate SRI hash for integrity checking
  const wasmPath = join(OUTPUT_DIR, 'edge-relays.wasm');
  if (existsSync(wasmPath)) {
    generateSRI(wasmPath);
  }
}

function generateCppSource(
  nonce: Buffer,
  encrypted: Buffer,
  obfuscatedKey: Buffer,
  relayCount: number
): string {
  return `// AUTO-GENERATED - DO NOT EDIT
// Build time: ${new Date().toISOString()}
// Relay count: ${relayCount}

#include <cstdint>
#include <cstring>
#include <string>

#include "edge-relays-data.h"

// ChaCha20 implementation (simplified for WASM)
static void chacha20_block(uint32_t output[16], const uint32_t input[16]) {
    for (int i = 0; i < 16; i++) output[i] = input[i];

    for (int i = 0; i < 10; i++) {
        // Quarter rounds
        #define QUARTERROUND(a,b,c,d) \\
            a += b; d ^= a; d = (d << 16) | (d >> 16); \\
            c += d; b ^= c; b = (b << 12) | (b >> 20); \\
            a += b; d ^= a; d = (d << 8) | (d >> 24); \\
            c += d; b ^= c; b = (b << 7) | (b >> 25);

        QUARTERROUND(output[0], output[4], output[8], output[12]);
        QUARTERROUND(output[1], output[5], output[9], output[13]);
        QUARTERROUND(output[2], output[6], output[10], output[14]);
        QUARTERROUND(output[3], output[7], output[11], output[15]);
        QUARTERROUND(output[0], output[5], output[10], output[15]);
        QUARTERROUND(output[1], output[6], output[11], output[12]);
        QUARTERROUND(output[2], output[7], output[8], output[13]);
        QUARTERROUND(output[3], output[4], output[9], output[14]);
    }

    for (int i = 0; i < 16; i++) output[i] += input[i];
}

static void chacha20_decrypt(uint8_t* output, const uint8_t* input, size_t len,
                             const uint8_t* key, const uint8_t* nonce) {
    uint32_t state[16] = {
        0x61707865, 0x3320646e, 0x79622d32, 0x6b206574,
        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
    };

    memcpy(&state[4], key, 32);
    memcpy(&state[14], nonce, 8);

    uint32_t keystream[16];
    for (size_t i = 0; i < len; i += 64) {
        state[12]++;
        chacha20_block(keystream, state);
        size_t chunk = (len - i < 64) ? (len - i) : 64;
        for (size_t j = 0; j < chunk; j++) {
            output[i + j] = input[i + j] ^ ((uint8_t*)keystream)[j];
        }
    }
}

static std::string cached_result;

extern "C" {
    const char* get_edge_relays() {
        if (!cached_result.empty()) {
            return cached_result.c_str();
        }

        // Deobfuscate key
        uint8_t key[32];
        for (int i = 0; i < 32; i++) {
            key[i] = KEY_MATERIAL[i] ^ KEY_MATERIAL[32 + i];
        }

        // Skip nonce (first 24 bytes) and decrypt
        const uint8_t* ciphertext = ENCRYPTED_RELAYS + 24;
        size_t ciphertext_len = ENCRYPTED_RELAYS_LEN - 24 - 16; // minus nonce and tag

        uint8_t* decrypted = new uint8_t[ciphertext_len + 1];
        chacha20_decrypt(decrypted, ciphertext, ciphertext_len, key, ENCRYPTED_RELAYS);
        decrypted[ciphertext_len] = 0;

        cached_result = std::string((char*)decrypted);
        delete[] decrypted;

        // Clear key from memory
        memset(key, 0, 32);

        return cached_result.c_str();
    }

    int get_relay_count() {
        const char* json = get_edge_relays();
        int count = 0;
        int slashes = 0;
        for (const char* p = json; *p; p++) {
            if (*p == '/') slashes++;
            if (*p == '"' && slashes > 0) {
                count++;
                slashes = 0;
            }
        }
        return count;
    }
}
`;
}

function generateHeaderSource(
  nonce: Buffer,
  encrypted: Buffer,
  obfuscatedKey: Buffer
): string {
  const ciphertext = Buffer.concat([nonce, encrypted]);

  const formatBytes = (buf: Buffer): string => {
    return Array.from(buf)
      .map(b => `0x${b.toString(16).padStart(2, '0')}`)
      .join(', ');
  };

  return `// AUTO-GENERATED - DO NOT EDIT
// Build time: ${new Date().toISOString()}

#pragma once

static const uint8_t ENCRYPTED_RELAYS[] = {
    ${formatBytes(ciphertext)}
};
static const size_t ENCRYPTED_RELAYS_LEN = ${ciphertext.length};

static const uint8_t KEY_MATERIAL[] = {
    ${formatBytes(obfuscatedKey)}
};
`;
}

function compileToWasm(cppPath: string): void {
  const wasmPath = join(OUTPUT_DIR, 'edge-relays.wasm');

  const cmd = [
    'emcc',
    cppPath,
    '-o', wasmPath,
    '-s', 'EXPORTED_FUNCTIONS=["_get_edge_relays", "_get_relay_count", "_malloc", "_free"]',
    '-s', 'EXPORTED_RUNTIME_METHODS=["UTF8ToString"]',
    '-s', 'WASM=1',
    '-s', 'MODULARIZE=1',
    '-s', 'EXPORT_NAME="EdgeRelaysModule"',
    '-O3',
    '--closure', '1',
  ].join(' ');

  console.log('Compiling to WASM...');
  execSync(cmd, { stdio: 'inherit' });
  console.log(`Generated ${wasmPath}`);
}

function generateSRI(wasmPath: string): void {
  const { createHash } = require('crypto');
  const content = readFileSync(wasmPath);
  const hash = createHash('sha384').update(content).digest('base64');
  const sri = `sha384-${hash}`;

  const sriPath = wasmPath + '.sri';
  writeFileSync(sriPath, sri);
  console.log(`SRI: ${sri}`);
  console.log(`Written to ${sriPath}`);
}

main().catch(err => {
  console.error('Error:', err);
  process.exit(1);
});
