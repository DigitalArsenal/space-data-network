#!/usr/bin/env node

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const KEY_WRAP_INFO = [
  Buffer.from("plugin-key-server-artifact-wrap-v1", "utf8"),
  Buffer.from("orbpro-key-server-artifact-wrap-v1", "utf8"),
];
const X25519_SPKI_PREFIX = Buffer.from("302a300506032b656e032100", "hex");
const X25519_PKCS8_PREFIX = Buffer.from(
  "302e020100300506032b656e04220420",
  "hex",
);
const SUPPORTED_FORMATS = [
  "plugin-key-server-encrypted-bundle-v2",
  "plugin-key-server-encrypted-bundle-v3",
  "orbpro-key-server-encrypted-bundle-v3",
];

function usage() {
  console.error(
    "Usage: node decrypt-plugin-license-artifact.mjs --artifact-dir <dir> --private-key <32-byte-hex> --output <file> [--loader-path <path>]",
  );
  process.exit(1);
}

function normalizeHex(value) {
  return String(value || "")
    .trim()
    .toLowerCase()
    .replace(/^0x/, "");
}

function parseHexBytes(value, expectedLength, label) {
  const normalized = normalizeHex(value);
  if (!/^[0-9a-f]+$/.test(normalized)) {
    throw new Error(`${label} must be hexadecimal`);
  }
  if (normalized.length !== expectedLength * 2) {
    throw new Error(
      `${label} must be ${expectedLength} bytes (${expectedLength * 2} hex chars)`,
    );
  }
  return Buffer.from(normalized, "hex");
}

function isValidX25519PrivateKeyHex(value) {
  return /^[0-9a-f]{64}$/.test(value);
}

function parseArgs(argv) {
  const args = {
    loaderPath: null,
    artifactDir: null,
    privateKey: null,
    outputPath: null,
  };

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--help" || arg === "-h") {
      usage();
    }

    if (arg.startsWith("--artifact-dir=")) {
      args.artifactDir = arg.slice("--artifact-dir=".length);
      continue;
    }
    if (arg.startsWith("--private-key=")) {
      args.privateKey = arg.slice("--private-key=".length);
      continue;
    }
    if (arg.startsWith("--output=")) {
      args.outputPath = arg.slice("--output=".length);
      continue;
    }
    if (arg.startsWith("--loader-path=")) {
      args.loaderPath = arg.slice("--loader-path=".length);
      continue;
    }

    if (arg === "--artifact-dir") {
      args.artifactDir = argv[i + 1];
      i += 1;
      continue;
    }
    if (arg === "--private-key") {
      args.privateKey = argv[i + 1];
      i += 1;
      continue;
    }
    if (arg === "--output") {
      args.outputPath = argv[i + 1];
      i += 1;
      continue;
    }
    if (arg === "--loader-path") {
      args.loaderPath = argv[i + 1];
      i += 1;
      continue;
    }

    throw new Error(`Unknown argument: ${arg}`);
  }

  return args;
}

function decodeBase64(value) {
  const normalized = String(value || "")
    .replace(/-/g, "+")
    .replace(/_/g, "/");
  return Buffer.from(normalized, "base64");
}

function createX25519PrivateKey(privateKeyHex) {
  const rawPrivateKey = parseHexBytes(
    privateKeyHex,
    32,
    "artifact private key",
  );
  return crypto.createPrivateKey({
    key: Buffer.concat([X25519_PKCS8_PREFIX, rawPrivateKey]),
    format: "der",
    type: "pkcs8",
  });
}

function createX25519PublicKey(rawHex) {
  const rawPublicKey = parseHexBytes(rawHex, 32, "ephemeral public key");
  return crypto.createPublicKey({
    key: Buffer.concat([X25519_SPKI_PREFIX, rawPublicKey]),
    format: "der",
    type: "spki",
  });
}

function unwrapContentKeyViaX25519(envelope, artifactPrivateKeyHex) {
  const artifactPrivateKey = createX25519PrivateKey(artifactPrivateKeyHex);
  const rawEphemeralPublicKey = envelope?.keyEncryption?.ephemeralPublicKeyHex;
  const ephemeralPublicKey = createX25519PublicKey(rawEphemeralPublicKey);
  const sharedSecret = crypto.diffieHellman({
    privateKey: artifactPrivateKey,
    publicKey: ephemeralPublicKey,
  });

  const hkdfSalt = decodeBase64(envelope?.keyEncryption?.hkdfSaltB64 || "");
  let wrapDecipherError = null;
  let wrappedKey = null;

  for (const keyWrapInfo of KEY_WRAP_INFO) {
    try {
      const wrapKey = crypto.hkdfSync(
        "sha256",
        sharedSecret,
        hkdfSalt,
        keyWrapInfo,
        32,
      );

      const wrapIv = decodeBase64(envelope?.keyEncryption?.wrapIvB64 || "");
      const wrappedRaw = decodeBase64(envelope?.keyEncryption?.wrappedKeyB64 || "");
      const wrappedKeyTag = decodeBase64(
        envelope?.keyEncryption?.wrappedKeyTagB64 || "",
      );

      const wrapDecipher = crypto.createDecipheriv("aes-256-gcm", wrapKey, wrapIv);
      wrapDecipher.setAuthTag(wrappedKeyTag);
      wrappedKey = Buffer.concat([
        wrapDecipher.update(wrappedRaw),
        wrapDecipher.final(),
      ]);
      return wrappedKey;
    } catch (error) {
      wrapDecipherError = error;
    }
  }

  if (wrapDecipherError) {
    throw wrapDecipherError;
  }
  throw new Error("failed to unwrap content key for x25519 envelope");
}

function unwrapContentKeyViaP256(envelope, artifactPrivateKeyHex) {
  const rawPrivateKey = parseHexBytes(
    artifactPrivateKeyHex,
    32,
    "artifact private key",
  );
  const ecdh = crypto.createECDH("prime256v1");
  ecdh.setPrivateKey(rawPrivateKey);
  const ephemeral = parseHexBytes(
    envelope?.keyEncryption?.ephemeralPublicKeyHex || "",
    32,
    "ephemeral public key",
  );

  const sharedSecret = ecdh.computeSecret(ephemeral);
  const hkdfSalt = decodeBase64(envelope?.keyEncryption?.hkdfSaltB64 || "");
  let wrapDecipherError = null;

  for (const keyWrapInfo of KEY_WRAP_INFO) {
    try {
      const wrapKey = crypto.hkdfSync(
        "sha256",
        sharedSecret,
        hkdfSalt,
        keyWrapInfo,
        32,
      );

      const wrapIv = decodeBase64(envelope?.keyEncryption?.wrapIvB64 || "");
      const wrappedKey = decodeBase64(
        envelope?.keyEncryption?.wrappedKeyB64 || "",
      );
      const wrappedKeyTag = decodeBase64(
        envelope?.keyEncryption?.wrappedKeyTagB64 || "",
      );

      const wrapDecipher = crypto.createDecipheriv(
        "aes-256-gcm",
        wrapKey,
        wrapIv,
      );
      wrapDecipher.setAuthTag(wrappedKeyTag);
      return Buffer.concat([
        wrapDecipher.update(wrappedKey),
        wrapDecipher.final(),
      ]);
    } catch (error) {
      wrapDecipherError = error;
    }
  }

  if (wrapDecipherError) {
    throw wrapDecipherError;
  }
  throw new Error("failed to unwrap content key for p256 envelope");
}

function decryptEnvelope(envelope, artifactPrivateKeyHex) {
  if (
    !envelope ||
    !envelope.keyEncryption ||
    !envelope.contentEncryption
  ) {
    throw new Error("invalid artifact envelope");
  }

  const scheme = String(envelope.keyEncryption.scheme || "");
  let contentKey;
  if (scheme === "ecies-x25519-hkdf-sha256-aes-256-gcm") {
    contentKey = unwrapContentKeyViaX25519(envelope, artifactPrivateKeyHex);
  } else if (scheme === "ecies-p256-hkdf-sha256-aes-256-gcm") {
    contentKey = unwrapContentKeyViaP256(envelope, artifactPrivateKeyHex);
  } else {
    throw new Error(`unsupported artifact scheme: ${scheme || "none"}`);
  }

  const contentIv = decodeBase64(envelope?.contentEncryption?.ivB64 || "");
  const contentCiphertext = decodeBase64(
    envelope?.contentEncryption?.ciphertextB64 || "",
  );
  const contentTag = decodeBase64(envelope?.contentEncryption?.tagB64 || "");

  const contentDecipher = crypto.createDecipheriv(
    "aes-256-gcm",
    contentKey,
    contentIv,
  );
  contentDecipher.setAuthTag(contentTag);
  return Buffer.concat([
    contentDecipher.update(contentCiphertext),
    contentDecipher.final(),
  ]);
}

function findEncryptedArtifactEntry(manifest) {
  if (!manifest || !Array.isArray(manifest.files)) {
    return null;
  }

  return (
    manifest.files.find((entry) =>
      String(entry?.outputFile || "") === "plugin-licensing-server.sdn.plugin",
    ) ||
    manifest.files.find((entry) =>
      String(entry?.path || "") === "dist/protection-key-server.wasm" ||
      String(entry?.path || "").endsWith("/protection-key-server.wasm"),
    ) ||
    null
  );
}

async function decryptWithLegacyFactory(artifactDir, privateKey, loaderPath, outputPath) {
  const loaderModule = await import(pathToFileURL(loaderPath).href);
  const fn = loaderModule.loadModuleFactoryFromEncryptedArtifacts;
  if (typeof fn !== "function") {
    return false;
  }

  const factory = await fn(artifactDir, privateKey);
  let cleanup = null;
  try {
    cleanup = factory.cleanup || null;
    const decryptedPluginPath = path.resolve(factory.locateFile("protection-key-server.wasm"));
    if (!fs.existsSync(decryptedPluginPath)) {
      throw new Error(`decrypted module not found: ${decryptedPluginPath}`);
    }

    await fs.promises.mkdir(path.dirname(outputPath), { recursive: true });
    await fs.promises.copyFile(decryptedPluginPath, outputPath);
    return true;
  } finally {
    if (typeof cleanup === "function") {
      cleanup();
    }
  }
}

function decryptWithDirectManifest(artifactDir, privateKey, outputPath, manifestPath) {
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  if (
    !manifest ||
    typeof manifest !== "object" ||
    !Array.isArray(manifest.files) ||
    !SUPPORTED_FORMATS.includes(manifest.format)
  ) {
    throw new Error(`unsupported manifest format in ${manifestPath}`);
  }

  const fileEntry = findEncryptedArtifactEntry(manifest);
  if (!fileEntry) {
  throw new Error("missing plugin-licensing-server.sdn.plugin entry");
  }

  const envelopePath = path.join(artifactDir, String(fileEntry.outputFile || ""));
  if (!fs.existsSync(envelopePath)) {
    throw new Error(`missing encrypted artifact file: ${envelopePath}`);
  }
  const envelope = JSON.parse(fs.readFileSync(envelopePath, "utf8"));
  const decryptedBuffer = decryptEnvelope(envelope, privateKey);

  fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  fs.writeFileSync(outputPath, decryptedBuffer);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));

  if (!args.artifactDir || !args.privateKey || !args.outputPath) {
    throw new Error("--artifact-dir, --private-key, and --output are required");
  }

  const artifactDir = path.resolve(String(args.artifactDir));
  const outputPath = path.resolve(String(args.outputPath));
  const privateKey = normalizeHex(args.privateKey);

  if (!isValidX25519PrivateKeyHex(privateKey)) {
    throw new Error("private key must be 32 bytes in hex format");
  }

  if (!fs.existsSync(artifactDir)) {
    throw new Error(`artifact directory not found: ${artifactDir}`);
  }

  const manifestPath = path.join(artifactDir, "manifest.json");
  if (!fs.existsSync(manifestPath)) {
    throw new Error(`manifest not found: ${manifestPath}`);
  }

  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  if (
    !manifest ||
    typeof manifest !== "object" ||
    !Array.isArray(manifest.files) ||
    !SUPPORTED_FORMATS.includes(manifest.format)
  ) {
    throw new Error(`unsupported manifest format in ${manifestPath}`);
  }

  const loaderPath = args.loaderPath
    ? path.resolve(args.loaderPath)
    : path.join(
        artifactDir,
        "..",
        "..",
        "packages",
        "plugins",
        "protection-key-server",
        "index.js",
      );

  if (!fs.existsSync(loaderPath)) {
    throw new Error(`loader not found: ${loaderPath}`);
  }

  const usedLegacyFlow = await decryptWithLegacyFactory(
    artifactDir,
    privateKey,
    loaderPath,
    outputPath,
  );

  if (!usedLegacyFlow) {
    decryptWithDirectManifest(
      artifactDir,
      privateKey,
      outputPath,
      manifestPath,
    );
  }

  if (!fs.existsSync(outputPath) || fs.statSync(outputPath).size === 0) {
    throw new Error(`decrypted artifact not written: ${outputPath}`);
  }

  process.stdout.write(`${outputPath}\n`);
}

main().catch((error) => {
  console.error(error.message);
  process.exit(1);
});
