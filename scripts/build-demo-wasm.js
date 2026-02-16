#!/usr/bin/env node
/**
 * SDN Encrypted WASM Demo Builder
 *
 * Encrypts the demo WASM module using the OrbPro KEK wrapper pattern:
 * 1. Generate random DEK (Data Encryption Key)
 * 2. Encrypt WASM payload with DEK via AES-256-GCM
 * 3. For each allowed domain: derive epoch-locked plugin key, wrap DEK
 * 4. Output: demo-payload.json (encrypted WASM + wrapped DEKs)
 *
 * The key broker server uses the same derivation secret + KDF to derive
 * the plugin key at runtime, so it can serve the correct key to clients.
 *
 * Usage:
 *   node scripts/build-demo-wasm.js
 *
 * Environment:
 *   DERIVATION_SECRET - Secret for key derivation (auto-generated if not set)
 *   DEMO_DOMAINS - Comma-separated allowed domains (default: localhost,127.0.0.1)
 *   DEMO_EPOCH_DAYS - Epoch period in days (default: 90)
 */

import fs from "fs";
import path from "path";
import crypto from "crypto";
import { fileURLToPath } from "url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const ROOT = path.resolve(__dirname, "..");

// ── Configuration ──────────────────────────────────────────────────────────

const PLUGIN_ID = "sdn-demo";
const WASM_PATH = path.join(ROOT, "demo", "demo.wasm");
const OUTPUT_PATH = path.join(ROOT, "demo", "demo-payload.json");

const domains = (process.env.DEMO_DOMAINS || "localhost,127.0.0.1").split(",").map(d => d.trim()).filter(Boolean);
const epochDays = parseInt(process.env.DEMO_EPOCH_DAYS || "90", 10);
const epochPeriodMs = epochDays * 24 * 60 * 60 * 1000;

// Load or generate derivation secret
let derivationSecret = process.env.DERIVATION_SECRET || "";
const secretsPath = path.join(ROOT, "demo", ".demo-secrets.json");
if (!derivationSecret) {
  if (fs.existsSync(secretsPath)) {
    const secrets = JSON.parse(fs.readFileSync(secretsPath, "utf8"));
    derivationSecret = secrets.derivationSecret || "";
  }
  if (!derivationSecret) {
    derivationSecret = crypto.randomBytes(64).toString("hex");
    fs.writeFileSync(secretsPath, JSON.stringify({ derivationSecret }, null, 2));
    console.log(`Generated new derivation secret → ${secretsPath}`);
  }
}

// Generate or load P-256 server private key
let serverPrivateKeyHex = process.env.ORBPRO_SERVER_PRIVATE_KEY_HEX || "";
if (!serverPrivateKeyHex) {
  if (fs.existsSync(secretsPath)) {
    const secrets = JSON.parse(fs.readFileSync(secretsPath, "utf8"));
    serverPrivateKeyHex = secrets.serverPrivateKeyHex || "";
  }
  if (!serverPrivateKeyHex) {
    // Generate P-256 private key
    const { privateKey } = crypto.generateKeyPairSync("ec", {
      namedCurve: "P-256",
    });
    const jwk = privateKey.export({ format: "jwk" });
    // d is base64url-encoded 32-byte private scalar
    serverPrivateKeyHex = Buffer.from(jwk.d, "base64url").toString("hex");
    // Save
    const existing = fs.existsSync(secretsPath) ? JSON.parse(fs.readFileSync(secretsPath, "utf8")) : {};
    existing.serverPrivateKeyHex = serverPrivateKeyHex;
    existing.derivationSecret = derivationSecret;
    fs.writeFileSync(secretsPath, JSON.stringify(existing, null, 2));
    console.log(`Generated new P-256 server key → ${secretsPath}`);
  }
}

// Compute server P-256 public key from private key hex using ECDH
const ecdh = crypto.createECDH("prime256v1");
ecdh.setPrivateKey(Buffer.from(serverPrivateKeyHex, "hex"));
const serverPubKeyUncompressed = ecdh.getPublicKey(); // 65 bytes: 0x04 + x + y
const serverPubKeyHex = serverPubKeyUncompressed.toString("hex");

console.log(`\nSDN Encrypted WASM Demo Builder`);
console.log(`==============================`);
console.log(`Plugin ID:     ${PLUGIN_ID}`);
console.log(`WASM:          ${WASM_PATH}`);
console.log(`Domains:       ${domains.join(", ")}`);
console.log(`Epoch:         ${epochDays} days (${epochPeriodMs}ms)`);
console.log(`Server PubKey: ${serverPubKeyHex.substring(0, 20)}...`);

// ── Crypto Helpers ──────────────────────────────────────────────────────────

function encryptGCM(plaintext, key) {
  const iv = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv("aes-256-gcm", key, iv);
  const ct = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  const tag = cipher.getAuthTag();
  return Buffer.concat([iv, ct, tag]); // IV(12) + ciphertext + tag(16)
}

function sha256(data) {
  return crypto.createHash("sha256").update(data).digest();
}

// ── KDF Program Generation ──────────────────────────────────────────────────
// Deterministic PRNG seeded from HMAC-SHA-256 of the derivation secret.
// Generates a bytecode program for data-driven key derivation.

function generateKdfProgram(secret) {
  const seed = crypto.createHmac("sha256", "kdf-program-v1").update(secret).digest();
  let counter = 0;

  function nextByte() {
    const hmac = crypto.createHmac("sha256", seed);
    const buf = Buffer.alloc(4);
    buf.writeUInt32LE(counter++);
    hmac.update(buf);
    return hmac.digest()[0];
  }

  function nextRange(min, max) {
    return min + (nextByte() % (max - min + 1));
  }

  const ops = [];
  for (let pass = 0; pass < 3; pass++) {
    // 5 required core operations
    const core = [
      [0, nextRange(0, 255), nextRange(0, 255)], // XOR
      [1, nextRange(1, 63), 0],                    // ROT
      [2, nextRange(0, 255), nextRange(0, 255)], // MUL
      [nextRange(3, 4), nextRange(0, 255), nextRange(0, 255)], // ADD or SUB
      [5, nextRange(0, 255), nextRange(0, 255)], // MIX
    ];
    // 1-2 NOP decoys
    const nopCount = nextRange(1, 2);
    for (let i = 0; i < nopCount; i++) {
      core.push([nextRange(6, 7), nextRange(0, 255), nextRange(0, 255)]);
    }
    // Fisher-Yates shuffle
    for (let i = core.length - 1; i > 0; i--) {
      const j = nextByte() % (i + 1);
      [core[i], core[j]] = [core[j], core[i]];
    }
    ops.push(...core);
  }

  const program = Buffer.alloc(ops.length * 3);
  for (let i = 0; i < ops.length; i++) {
    program[i * 3] = ops[i][0];
    program[i * 3 + 1] = ops[i][1];
    program[i * 3 + 2] = ops[i][2];
  }
  return program;
}

// ── Key Derivation ──────────────────────────────────────────────────────────
// Derives a 32-byte AES-256 key from secret + domain + epoch + plugin ID.
// Must match the C-side key_broker.cpp derive_plugin_key() implementation.

function deriveKey(secret, pluginId, domainHashBuf, epoch) {
  const secretBuf = Buffer.from(secret, "utf8");
  const kdfProgram = generateKdfProgram(secret);

  // Start with raw secret bytes as base key (32 bytes)
  let key = sha256(secretBuf);

  // 3-pass mixing (simplified JS version matching C-side)
  for (let pass = 0; pass < 3; pass++) {
    for (let i = 0; i < 32; i++) {
      const rotAmount = ((i * 7 + 3) % 7) + 1;
      key[i] = key[i] ^ secretBuf[i % secretBuf.length];
      key[i] = ((key[i] << rotAmount) | (key[i] >>> (8 - rotAmount))) & 0xff;
      if (i > 0) key[i] ^= key[i - 1]; // CBC chain
    }
  }

  // Apply KDF program on uint64 blocks
  const view = new DataView(key.buffer, key.byteOffset, key.byteLength);
  for (let block = 0; block < 4; block++) {
    let val = view.getBigUint64(block * 8, true);
    for (let i = 0; i < kdfProgram.length; i += 3) {
      const op = kdfProgram[i];
      const argLo = kdfProgram[i + 1];
      const argHi = kdfProgram[i + 2];
      const arg = BigInt((argHi << 8) | argLo);
      const MASK = 0xffffffffffffffffn;
      switch (op) {
        case 0: val = (val ^ (arg * 0x9e3779b185ebca87n)) & MASK; break;
        case 1: { const r = BigInt(argLo % 63 + 1); val = ((val << r) | (val >> (64n - r))) & MASK; break; }
        case 2: val = (val * ((arg << 1n) | 1n)) & MASK; break;
        case 3: val = (val + arg) & MASK; break;
        case 4: val = (val - arg) & MASK; break;
        case 5: val = (val ^ ((val >> 17n) * 0xbf58476d1ce4e5b9n)) & MASK; break;
        default: break; // NOP
      }
    }
    view.setBigUint64(block * 8, val, true);
  }

  // Domain hash XOR
  if (domainHashBuf && domainHashBuf.length === 32) {
    for (let i = 0; i < 32; i++) {
      key[i] ^= domainHashBuf[i];
    }
  }

  // Epoch mixing: SHA-256(epoch as 8-byte LE) XOR'd into key
  if (epoch !== undefined) {
    const epochBuf = Buffer.alloc(8);
    epochBuf.writeBigUInt64LE(BigInt(epoch));
    const epochHash = sha256(epochBuf);
    for (let i = 0; i < 32; i++) {
      key[i] ^= epochHash[i];
    }
  }

  // Plugin ID diversification: SHA-256(key || pluginId)
  if (pluginId) {
    key = sha256(Buffer.concat([key, Buffer.from(pluginId, "utf8")]));
  }

  return key;
}

// ── Main ──────────────────────────────────────────────────────────────────

function main() {
  if (!fs.existsSync(WASM_PATH)) {
    console.error(`ERROR: demo.wasm not found at ${WASM_PATH}`);
    console.error(`Run: cd demo && emcc demo.c -o demo.wasm -s STANDALONE_WASM=1 --no-entry -O2`);
    process.exit(1);
  }

  const wasmBytes = fs.readFileSync(WASM_PATH);
  console.log(`\nWASM size: ${wasmBytes.length} bytes`);

  // Generate random DEK
  const dek = crypto.randomBytes(32);

  // Encrypt WASM payload with DEK
  console.log("Encrypting WASM payload with AES-256-GCM...");
  const encryptedPayload = encryptGCM(wasmBytes, dek);
  console.log(`  Encrypted: ${encryptedPayload.length} bytes (IV + ciphertext + tag)`);

  // Current epoch
  const currentEpoch = Math.floor(Date.now() / epochPeriodMs);
  console.log(`  Current epoch: ${currentEpoch}`);

  // Wrap DEK for each domain
  console.log("\nWrapping DEK for domains:");
  const wrappedDEKs = [];
  for (const domain of domains) {
    const domainHash = sha256(Buffer.from(domain));
    const pluginKey = deriveKey(derivationSecret, PLUGIN_ID, domainHash, currentEpoch);
    const wrappedDEK = encryptGCM(dek, pluginKey);
    wrappedDEKs.push({
      domain,
      domainHash: domainHash.toString("hex"),
      wrappedDEK: wrappedDEK.toString("base64"),
    });
    console.log(`  ${domain}: ${wrappedDEK.length} bytes → base64`);
  }

  // Wipe DEK from memory
  dek.fill(0);

  // Build output payload
  const payload = {
    version: 1,
    pluginId: PLUGIN_ID,
    epochPeriodMs,
    currentEpoch,
    createdAt: new Date().toISOString(),
    serverPublicKeyHex: serverPubKeyHex,
    encryptedPayload: encryptedPayload.toString("base64"),
    wrappedDEKs: wrappedDEKs.map(w => w.wrappedDEK),
    domains: wrappedDEKs.map(w => w.domain),
    wasmSize: wasmBytes.length,
  };

  fs.writeFileSync(OUTPUT_PATH, JSON.stringify(payload, null, 2));
  console.log(`\nOutput: ${OUTPUT_PATH}`);
  console.log(`  Payload size: ${fs.statSync(OUTPUT_PATH).size} bytes`);

  // Also write a .env file for the server
  const envPath = path.join(ROOT, "demo", ".demo-env");
  const envContent = [
    `ORBPRO_SERVER_PRIVATE_KEY_HEX=${serverPrivateKeyHex}`,
    `DERIVATION_SECRET=${derivationSecret}`,
    `ORBPRO_KEYSERVER_ALLOWED_DOMAINS=${domains.join(",")}`,
    `ORBPRO_KEYSERVER_EPOCH_PERIOD_MS=${epochPeriodMs}`,
    `ORBPRO_KEYSERVER_MAX_SKEW_MS=300000`,
    `ORBPRO_KEYSERVER_LEASE_MS=300000`,
  ].join("\n");
  fs.writeFileSync(envPath, envContent);
  console.log(`  Env file: ${envPath}`);

  console.log("\nDone! The server can now serve the encrypted payload.");
  console.log("To start with demo env: source demo/.demo-env && ./scripts/dev-local.sh");
}

main();
