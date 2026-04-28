#!/usr/bin/env node

import fsSync from "node:fs";
import fs from "node:fs/promises";
import crypto from "node:crypto";
import path from "node:path";
import process from "node:process";
import { fileURLToPath, pathToFileURL } from "node:url";
import { protectModuleArtifact } from "space-data-module-sdk";
import {
  extractPublicationRecordCollection,
  generateX25519Keypair,
} from "space-data-module-sdk/transport";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(__dirname, "..");
const repoRoot = path.resolve(packageRoot, "..", "..");
const workspaceRoot = path.resolve(repoRoot, "..");
const defaultCacheControl =
  "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400";
const defaultContentType = "application/wasm+encrypted";
const defaultRequiredScope = "orbpro:base";
const defaultVersion = "local-dev";
const defaultLocalSignerPublicKeyHex = "6c".repeat(32);
const currentSpaceDataNetworkModulesVersion = "0.1.0-0.8.2";
const orbproProtectedContextPrefix = "orbpro.plugin/";
const moduleIdPattern = /^[A-Za-z0-9._-]+$/;
const providerContentKeyEnvelopeContext =
  "space-data-network/plugin-module/content-key/v1";
const providerContentKeyEnvelopeAlgorithm =
  "X25519-HKDF-SHA256-AES-256-GCM";
const X25519_SPKI_PREFIX = Buffer.from("302a300506032b656e032100", "hex");
const X25519_PKCS8_PREFIX = Buffer.from(
  "302e020100300506032b656e04220420",
  "hex",
);
const defaultAllowedDomains = Object.freeze([
  "localhost",
  "127.0.0.1",
  "spaceaware.io",
  "www.spaceaware.io",
  "digitalarsenal.io",
  "www.digitalarsenal.io",
]);
const staleManagedModuleIds = Object.freeze([
  "licensing",
  "conjunction-assessment",
]);

const DEFAULT_ORBPRO_MODULES = Object.freeze([
  Object.freeze({
    slug: "orbpro-licensing",
    moduleId: "com.orbpro.licensing",
    requiredScope: "orbpro:license",
    wasmPath:
      "../space-data-network-plugins/packages/licensing/dist/isomorphic/module.wasm",
    manifestPath:
      "../space-data-network-plugins/packages/licensing/plugin-manifest.json",
  }),
  Object.freeze({
    slug: "viewshed-shader",
    protectedModulePath:
      "packages/orbpro-integration/viewshed-shader/dist/viewshed-shader-data.js",
    protectedExports: Object.freeze([
      Object.freeze({ exportName: "encryptedData", slug: "viewshed-shader" }),
    ]),
    keyExport: "recipientPrivateKeyHex",
  }),
  Object.freeze({
    slug: "viewshed-shader-assets",
    protectedModulePath:
      "packages/orbpro-integration/viewshed-shader/dist/viewshed-shader-encrypted.js",
    protectedExports: Object.freeze([
      Object.freeze({
        exportName: "vertexFragments",
        slugPrefix: "viewshed-shader-vertex-fragment",
      }),
      Object.freeze({
        exportName: "fragmentFragments",
        slugPrefix: "viewshed-shader-fragment-fragment",
      }),
      Object.freeze({
        exportName: "frustumVertexFragments",
        slugPrefix: "viewshed-shader-frustum-vertex-fragment",
      }),
      Object.freeze({
        exportName: "frustumFragmentFragments",
        slugPrefix: "viewshed-shader-frustum-fragment-fragment",
      }),
      Object.freeze({
        exportName: "encryptedUniforms",
        slug: "viewshed-shader-uniforms",
      }),
    ]),
    keyExport: "recipientPrivateKeyHex",
  }),
  Object.freeze({
    slug: "sensor-shaders",
    protectedModulePath:
      "packages/orbpro-integration/shader.sensor-shaders/dist/sensor-shaders-data.js",
    protectedExports: Object.freeze([
      Object.freeze({ exportName: "encryptedData", slug: "sensor-shaders" }),
    ]),
    keyExport: "recipientPrivateKeyHex",
  }),
  Object.freeze({
    slug: "sensor-shaders-assets",
    protectedModulePath:
      "packages/orbpro-integration/shader.sensor-shaders/dist/sensor-shaders-encrypted.js",
    protectedExports: Object.freeze([
      Object.freeze({
        exportName: "encryptedData",
        slug: "sensor-shaders-glsl-bundle",
      }),
    ]),
    keyExport: "recipientPrivateKeyHex",
  }),
  Object.freeze({
    slug: "sgp4",
    protectedModulePath:
      "packages/orbpro-integration/propagator.sgp4/dist/sgp4-encrypted.js",
    protectedExports: Object.freeze([
      Object.freeze({ exportName: "encryptedData", slug: "sgp4" }),
    ]),
    keyExport: "recipientPrivateKeyHex",
  }),
  Object.freeze({
    slug: "fastest-path",
    protectedModulePath:
      "packages/orbpro-integration/analysis.fastest-path/dist/fastest-path-encrypted.js",
    protectedExports: Object.freeze([
      Object.freeze({ exportName: "encryptedData", slug: "fastest-path" }),
    ]),
    keyExport: "recipientPrivateKeyHex",
  }),
  Object.freeze({
    slug: "hpop",
    protectedModulePath:
      "packages/orbpro-integration/propagator.hpop/dist/hpop-encrypted.js",
    protectedExports: Object.freeze([
      Object.freeze({ exportName: "encryptedData", slug: "hpop" }),
    ]),
    keyExport: "recipientPrivateKeyHex",
  }),
  Object.freeze({
    slug: "access",
    moduleId: "com.orbpro.access",
    wasmPath:
      "packages/space-data-network-modules/analysis/access/dist/isomorphic/module.wasm",
    manifestPath:
      "packages/space-data-network-modules/analysis/access/plugin-manifest.json",
  }),
]);

const OPTIONAL_ORBPRO_MODULES = Object.freeze([
  Object.freeze({
    slug: "conjunction-assessment",
    moduleId: "org.spacedata.analysis.conjunction.assessment",
    version: currentSpaceDataNetworkModulesVersion,
    wasmPath:
      "packages/space-data-network-modules/analysis/conjunction-assessment/dist/isomorphic/module.wasm",
    manifestPath:
      "packages/space-data-network-modules/analysis/conjunction-assessment/plugin-manifest.json",
  }),
]);
function resolveModuleVersion(moduleSpec, manifest) {
  const explicitVersion = String(moduleSpec?.version || "").trim();
  if (explicitVersion) {
    return explicitVersion;
  }
  const manifestVersion = String(manifest?.version || "").trim();
  return manifestVersion || defaultVersion;
}

function usage() {
  console.error(`Usage:
  node scripts/seed-orbpro-module-catalog.mjs --plugin-root <path>
  node scripts/seed-orbpro-module-catalog.mjs --storage-path <path>

Options:
  --plugin-root <path>              Exact plugin root (catalog.json lives here)
  --storage-path <path>             Storage root; helper writes to <storage>/license/plugins
  --provider-x25519-pubkey <key>    Provider module upload X25519 public key, base64url
  --provider-peer-id <peer-id>      Provider libp2p peer id
  --with-conjunction                Include the standalone conjunction plugin
  --json                            Print the final summary JSON only
  --help                            Show this message
`);
}

function parseArgs(argv) {
  const options = {
    pluginRoot: "",
    storagePath: "",
    providerX25519PubKey: "",
    providerPeerID: "",
    withConjunction: false,
    json: false,
  };

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    switch (arg) {
      case "--plugin-root":
        options.pluginRoot = argv[index + 1] ?? "";
        index += 1;
        break;
      case "--storage-path":
        options.storagePath = argv[index + 1] ?? "";
        index += 1;
        break;
      case "--provider-x25519-pubkey":
        options.providerX25519PubKey = argv[index + 1] ?? "";
        index += 1;
        break;
      case "--provider-peer-id":
        options.providerPeerID = argv[index + 1] ?? "";
        index += 1;
        break;
      case "--with-conjunction":
        options.withConjunction = true;
        break;
      case "--json":
        options.json = true;
        break;
      case "--help":
      case "-h":
        usage();
        process.exit(0);
        break;
      default:
        throw new Error(`Unknown argument: ${arg}`);
    }
  }

  return options;
}

function resolvePluginRoot({ pluginRoot, storagePath } = {}) {
  const explicitPluginRoot = String(
    pluginRoot || process.env.SDN_PLUGIN_ROOT || "",
  ).trim();
  if (explicitPluginRoot) {
    return path.resolve(explicitPluginRoot);
  }

  const explicitStoragePath = String(storagePath || "").trim();
  if (explicitStoragePath) {
    return path.resolve(explicitStoragePath, "license", "plugins");
  }

  throw new Error(
    "A plugin root is required. Pass --plugin-root, --storage-path, or set SDN_PLUGIN_ROOT.",
  );
}

function resolveModulePath(relativeOrAbsolutePath, label = "Module path") {
  const rawPath = String(relativeOrAbsolutePath || "").trim();
  if (!rawPath) {
    throw new Error(`${label} is required.`);
  }
  if (path.isAbsolute(rawPath)) {
    return rawPath;
  }
  const candidatePaths = [
    path.resolve(repoRoot, rawPath),
    path.resolve(workspaceRoot, rawPath),
  ];
  const existingPath = candidatePaths.find((candidatePath) =>
    fsSync.existsSync(candidatePath),
  );
  if (existingPath) {
    return existingPath;
  }
  throw new Error(
    `${label} not found for "${rawPath}". Tried ${candidatePaths.join(", ")}`,
  );
}

function resolveManifestPath(moduleSpec) {
  const explicitPath = String(moduleSpec?.manifestPath || "").trim();
  if (explicitPath) {
    return resolveModulePath(explicitPath, "Plugin manifest path");
  }

  const wasmPath = resolveModulePath(moduleSpec?.wasmPath, "Module wasmPath");
  const candidatePaths = [
    path.resolve(path.dirname(wasmPath), "manifest.json"),
    path.resolve(path.dirname(wasmPath), "..", "manifest.json"),
    path.resolve(path.dirname(wasmPath), "..", "plugin-manifest.json"),
    path.resolve(path.dirname(wasmPath), "..", "..", "plugin-manifest.json"),
  ];
  return (
    candidatePaths.find((candidatePath) => fsSync.existsSync(candidatePath)) ||
    candidatePaths[0]
  );
}

async function readCatalog(pluginRoot) {
  const catalogPath = path.join(pluginRoot, "catalog.json");
  try {
    const raw = await fs.readFile(catalogPath, "utf8");
    const parsed = JSON.parse(raw);
    return {
      catalogPath,
      plugins: Array.isArray(parsed?.plugins) ? parsed.plugins : [],
    };
  } catch (error) {
    if (error?.code === "ENOENT") {
      return { catalogPath, plugins: [] };
    }
    throw error;
  }
}

function upsertCatalogEntry(entries, nextEntry) {
  const filtered = entries.filter((entry) => entry?.id !== nextEntry.id);
  filtered.push(nextEntry);
  filtered.sort((left, right) =>
    String(left?.id || "").localeCompare(String(right?.id || "")),
  );
  return filtered;
}

function buildCatalogEntry(moduleSpec) {
  const moduleID = String(moduleSpec.moduleId || "").trim();
  const version = String(moduleSpec.version || defaultVersion).trim();
  const allowedDomains = normalizeAllowedDomains(moduleSpec.allowedDomains);
  return {
    id: moduleID,
    version,
    required_scope: moduleSpec.requiredScope || defaultRequiredScope,
    encrypted_path: `${moduleID}/bundle.wasm.enc`,
    key_envelope_path: `${moduleID}/bundle.key-envelope.json`,
    content_type: moduleSpec.contentType || defaultContentType,
    cache_control: moduleSpec.cacheControl || defaultCacheControl,
    allowed_domains: allowedDomains,
    signer_pubkey_hex:
      normalizeHex32(moduleSpec.signerPublicKeyHex, "signer public key") ||
      defaultLocalSignerPublicKeyHex,
  };
}

function normalizeModuleSpec(moduleSpec) {
  const slug = String(moduleSpec?.slug || "").trim();
  const moduleId = String(moduleSpec?.moduleId || "").trim();
  if (!slug) {
    throw new Error("Module slug is required.");
  }
  if (!moduleId) {
    throw new Error(`Module id is required for slug "${slug}".`);
  }
  return {
    ...moduleSpec,
    slug,
    moduleId,
    wasmPath: resolveModulePath(moduleSpec?.wasmPath, "Module wasmPath"),
    manifestPath: resolveManifestPath(moduleSpec),
  };
}

function decodeBase64Bytes(value, context) {
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new Error(`${context} must be a non-empty base64 string.`);
  }
  return Buffer.from(value, "base64");
}

function normalizeCatalogModuleId(value) {
  const raw = String(value || "").trim();
  if (!raw) {
    return "";
  }
  const withoutPrefix = raw.startsWith(orbproProtectedContextPrefix)
    ? raw.slice(orbproProtectedContextPrefix.length)
    : raw;
  const normalized = withoutPrefix
    .replace(/[\\/]+/g, ".")
    .replace(/[^A-Za-z0-9._-]+/g, "-")
    .replace(/\.+/g, ".")
    .replace(/^[.-]+|[.-]+$/g, "");
  return moduleIdPattern.test(normalized) ? normalized : "";
}

function normalizeArtifactSlug(value, fallback) {
  const raw = String(value || fallback || "").trim();
  const slug = raw
    .replace(/[\\/]+/g, "-")
    .replace(/[^A-Za-z0-9._-]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^[.-]+|[.-]+$/g, "");
  if (!slug) {
    throw new Error("Protected artifact slug resolved to an empty value.");
  }
  return slug;
}

function resolveProtectedVersion(moduleSpec, exportSpec, publication) {
  const explicitVersion = String(
    exportSpec?.version || moduleSpec?.version || "",
  ).trim();
  if (explicitVersion) {
    return explicitVersion;
  }
  const publicationVersion = String(publication?.version || "").trim();
  return publicationVersion || defaultVersion;
}

function resolveProtectedModuleId(moduleSpec, exportSpec, publication, slug) {
  const explicit = normalizeCatalogModuleId(
    exportSpec?.moduleId || moduleSpec?.moduleId || "",
  );
  if (explicit) {
    return explicit;
  }
  const publicationContext = normalizeCatalogModuleId(
    publication?.enc?.context || publication?.pnm?.fileId || "",
  );
  if (publicationContext) {
    return publicationContext;
  }
  throw new Error(
    `Unable to derive SDN module id for protected artifact "${slug}".`,
  );
}

function normalizeRecipientPrivateKeyHex(value, context) {
  const normalized = normalizeHex(value);
  if (!/^[0-9a-f]{64}$/.test(normalized)) {
    throw new Error(`${context} must export a 32-byte X25519 private key hex string.`);
  }
  return normalized;
}

function normalizeHex(value) {
  return String(value || "")
    .trim()
    .toLowerCase()
    .replace(/^0x/, "");
}

function normalizeHex32(value, context) {
  const normalized = normalizeHex(value);
  if (!normalized) {
    return "";
  }
  if (!/^[0-9a-f]{64}$/.test(normalized)) {
    throw new Error(`${context} must be a 32-byte hex string.`);
  }
  return normalized;
}

function normalizeAllowedDomains(value) {
  const source = Array.isArray(value) && value.length > 0
    ? value
    : defaultAllowedDomains;
  return Array.from(
    new Set(
      source
        .map((entry) => String(entry || "").trim().toLowerCase())
        .filter(Boolean),
    ),
  );
}

function normalizeProviderEnvelopeConfig({
  providerX25519PubKey,
  providerPeerID,
  required = false,
} = {}) {
  const peerID = String(providerPeerID || "").trim();
  const encodedPublicKey = String(providerX25519PubKey || "").trim();
  if (!peerID || !encodedPublicKey) {
    if (!required) {
      return null;
    }
    throw new Error(
      "Provider X25519 public key and peer ID are required. Pass --provider-x25519-pubkey and --provider-peer-id.",
    );
  }
  const publicKey = Buffer.from(encodedPublicKey, "base64url");
  if (publicKey.length !== 32) {
    throw new Error("--provider-x25519-pubkey must decode to 32 bytes.");
  }
  return {
    providerPeerID: peerID,
    providerPublicKey: publicKey,
    providerPublicKeyBase64URL: publicKey.toString("base64url"),
  };
}

async function writeSeededEncryptedModule({
  pluginRoot,
  moduleSpec,
  encryptedBytes,
  keyHex,
  publication,
  providerConfig,
}) {
  if (!providerConfig) {
    throw new Error("Provider envelope config is required.");
  }
  const entry = buildCatalogEntry(moduleSpec);
  const keyBytes = Buffer.from(normalizeRecipientPrivateKeyHex(
    keyHex,
    `${moduleSpec.moduleId} content key`,
  ), "hex");
  const bundleSHA256 = crypto
    .createHash("sha256")
    .update(encryptedBytes)
    .digest("hex");
  const envelope = wrapProviderContentKey(keyBytes, providerConfig, {
    moduleID: entry.id,
    version: entry.version,
    bundleSHA256,
    signerPublicKeyHex: entry.signer_pubkey_hex,
  });
  keyBytes.fill(0);

  const encryptedPath = path.join(pluginRoot, entry.encrypted_path);
  const keyEnvelopePath = path.join(pluginRoot, entry.key_envelope_path);
  await fs.mkdir(path.dirname(encryptedPath), { recursive: true, mode: 0o700 });
  await fs.writeFile(encryptedPath, encryptedBytes, { mode: 0o600 });
  await fs.writeFile(
    keyEnvelopePath,
    `${JSON.stringify(envelope, null, 2)}\n`,
    { mode: 0o600 },
  );
  await removeLegacySeedFiles(pluginRoot, moduleSpec);

  return {
    entry,
    summary: {
      moduleId: entry.id,
      version: entry.version,
      encryptedPath,
      keyEnvelopePath,
      encryptedSizeBytes: encryptedBytes.length,
      bundleSHA256,
      allowedDomains: entry.allowed_domains.slice(),
      hasMbl: Boolean(publication?.mbl),
      hasEnc: Boolean(publication?.enc),
      hasPnm: Boolean(publication?.pnm),
    },
  };
}

function wrapProviderContentKey(contentKey, providerConfig, aadInput) {
  if (contentKey.length !== 32) {
    throw new Error(`content key must be 32 bytes, got ${contentKey.length}`);
  }
  const aad = {
    module_id: aadInput.moduleID,
    version: aadInput.version,
    bundle_sha256: aadInput.bundleSHA256,
    signer_public_key_hex: aadInput.signerPublicKeyHex,
    provider_peer_id: providerConfig.providerPeerID,
  };
  const aadBytes = Buffer.from(JSON.stringify(aad), "utf8");
  const ephemeralPrivateRaw = crypto.randomBytes(32);
  let sharedSecret = null;
  let wrapKey = null;
  try {
    const ephemeralPrivateKey = createX25519PrivateKey(ephemeralPrivateRaw);
    const ephemeralPublicKey = crypto.createPublicKey(ephemeralPrivateKey);
    const ephemeralPublicRaw = exportRawX25519PublicKey(ephemeralPublicKey);
    const providerPublicKey = createX25519PublicKey(providerConfig.providerPublicKey);
    sharedSecret = crypto.diffieHellman({
      privateKey: ephemeralPrivateKey,
      publicKey: providerPublicKey,
    });
    wrapKey = Buffer.from(crypto.hkdfSync(
      "sha256",
      sharedSecret,
      Buffer.alloc(0),
      Buffer.from(providerContentKeyEnvelopeContext, "utf8"),
      32,
    ));
    const nonce = crypto.randomBytes(12);
    const cipher = crypto.createCipheriv("aes-256-gcm", wrapKey, nonce);
    cipher.setAAD(aadBytes);
    const ciphertext = Buffer.concat([
      cipher.update(contentKey),
      cipher.final(),
      cipher.getAuthTag(),
    ]);

    return {
      version: 1,
      alg: providerContentKeyEnvelopeAlgorithm,
      context: providerContentKeyEnvelopeContext,
      provider_x25519_pubkey: providerConfig.providerPublicKeyBase64URL,
      ephemeral_x25519_pubkey: ephemeralPublicRaw.toString("base64url"),
      nonce: nonce.toString("base64url"),
      aad: aadBytes.toString("base64url"),
      ciphertext: ciphertext.toString("base64url"),
    };
  } finally {
    ephemeralPrivateRaw.fill(0);
    sharedSecret?.fill?.(0);
    wrapKey?.fill?.(0);
  }
}

function createX25519PrivateKey(rawPrivateKey) {
  return crypto.createPrivateKey({
    key: Buffer.concat([X25519_PKCS8_PREFIX, rawPrivateKey]),
    format: "der",
    type: "pkcs8",
  });
}

function createX25519PublicKey(rawPublicKey) {
  return crypto.createPublicKey({
    key: Buffer.concat([X25519_SPKI_PREFIX, rawPublicKey]),
    format: "der",
    type: "spki",
  });
}

function exportRawX25519PublicKey(publicKey) {
  const der = publicKey.export({ format: "der", type: "spki" });
  return Buffer.from(der.subarray(-32));
}

async function removeLegacySeedFiles(pluginRoot, moduleSpec) {
  const candidates = [
    path.join(pluginRoot, `${moduleSpec.slug}.key`),
    path.join(pluginRoot, `${moduleSpec.slug}.wasm.enc`),
    path.join(pluginRoot, moduleSpec.moduleId, "bundle.key"),
  ];
  await Promise.all(candidates.map(removeFileIfExists));
}

async function removeFileIfExists(filePath) {
  try {
    await fs.rm(filePath, { force: true });
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

function resolveProtectedExportArtifacts(exportSpec, exportedValue, moduleSpec) {
  if (Array.isArray(exportedValue)) {
    const slugPrefix = normalizeArtifactSlug(
      exportSpec?.slugPrefix || exportSpec?.slug,
      `${moduleSpec.slug}-${exportSpec.exportName}`,
    );
    return exportedValue.map((value, index) => ({
      value,
      index,
      slug: normalizeArtifactSlug(`${slugPrefix}-${index}`, ""),
    }));
  }

  return [
    {
      value: exportedValue,
      index: null,
      slug: normalizeArtifactSlug(
        exportSpec?.slug || exportSpec?.slugPrefix,
        `${moduleSpec.slug}-${exportSpec.exportName}`,
      ),
    },
  ];
}

async function expandProtectedModuleSpec(rawModuleSpec) {
  const slug = normalizeArtifactSlug(rawModuleSpec?.slug, "");
  const protectedModulePath = resolveModulePath(
    rawModuleSpec?.protectedModulePath,
    "Protected module path",
  );
  const protectedModule = await import(pathToFileURL(protectedModulePath).href);
  const keyExport = String(rawModuleSpec?.keyExport || "").trim();
  if (!keyExport) {
    throw new Error(`Protected module "${slug}" requires keyExport.`);
  }
  const keyHex = normalizeRecipientPrivateKeyHex(
    protectedModule[keyExport],
    `${protectedModulePath} export ${keyExport}`,
  );
  const protectedExports = Array.isArray(rawModuleSpec?.protectedExports)
    ? rawModuleSpec.protectedExports
    : [];
  if (protectedExports.length === 0) {
    throw new Error(`Protected module "${slug}" has no protectedExports.`);
  }

  const expanded = [];
  for (const exportSpec of protectedExports) {
    const exportName = String(exportSpec?.exportName || "").trim();
    if (!exportName) {
      throw new Error(`Protected module "${slug}" has an export with no exportName.`);
    }
    if (!Object.hasOwn(protectedModule, exportName)) {
      throw new Error(
        `Protected module "${protectedModulePath}" is missing export "${exportName}".`,
      );
    }

    const artifacts = resolveProtectedExportArtifacts(
      exportSpec,
      protectedModule[exportName],
      { ...rawModuleSpec, slug },
    );
    for (const artifact of artifacts) {
      const encryptedBytes = decodeBase64Bytes(
        artifact.value,
        `${protectedModulePath} export ${exportName}`,
      );
      const publication = extractPublicationRecordCollection(encryptedBytes);
      const moduleId = resolveProtectedModuleId(
        rawModuleSpec,
        exportSpec,
        publication,
        artifact.slug,
      );
      expanded.push({
        ...rawModuleSpec,
        kind: "protected",
        slug: artifact.slug,
        moduleId,
        version: resolveProtectedVersion(rawModuleSpec, exportSpec, publication),
        protectedModulePath,
        exportName,
        artifactIndex: artifact.index,
        encryptedBytes,
        keyHex,
        publication,
      });
    }
  }

  return expanded;
}

async function expandModuleSpecs(modules) {
  const expanded = [];
  for (const rawModule of modules) {
    if (rawModule?.protectedModulePath) {
      expanded.push(...(await expandProtectedModuleSpec(rawModule)));
    } else {
      expanded.push({ ...normalizeModuleSpec(rawModule), kind: "raw" });
    }
  }
  return expanded;
}

async function loadModuleManifest(moduleSpec) {
  const candidatePaths = [
    moduleSpec.manifestPath,
    path.resolve(path.dirname(moduleSpec.wasmPath), "manifest.json"),
    path.resolve(path.dirname(moduleSpec.wasmPath), "..", "manifest.json"),
    path.resolve(path.dirname(moduleSpec.wasmPath), "..", "plugin-manifest.json"),
    path.resolve(path.dirname(moduleSpec.wasmPath), "..", "..", "plugin-manifest.json"),
  ];

  for (const candidatePath of candidatePaths) {
    try {
      const raw = await fs.readFile(candidatePath, "utf8");
      return JSON.parse(raw);
    } catch (error) {
      if (error?.code !== "ENOENT") {
        throw new Error(
          `Unable to read plugin manifest for ${moduleSpec.moduleId} from ${candidatePath}: ${error.message || error}`,
        );
      }
    }
  }

  throw new Error(
    `No plugin manifest found for ${moduleSpec.moduleId}; tried ${candidatePaths.join(", ")}`,
  );
}

export async function seedOrbproModuleCatalog({
  pluginRoot,
  storagePath,
  modules = DEFAULT_ORBPRO_MODULES,
  providerX25519PubKey,
  providerPeerID,
} = {}) {
  const resolvedPluginRoot = resolvePluginRoot({ pluginRoot, storagePath });
  await fs.mkdir(resolvedPluginRoot, { recursive: true });

  const { catalogPath, plugins: existingPlugins } = await readCatalog(
    resolvedPluginRoot,
  );
  const seeded = [];
  const expandedModules = await expandModuleSpecs(modules);
  const providerConfig = normalizeProviderEnvelopeConfig({
    providerX25519PubKey,
    providerPeerID,
    required: expandedModules.length > 0,
  });
  const managedModuleIds = new Set([
    ...staleManagedModuleIds,
    ...expandedModules.map((moduleSpec) => moduleSpec.moduleId),
  ]);
  let plugins = existingPlugins.filter(
    (entry) => !managedModuleIds.has(String(entry?.id || "").trim()),
  );

  for (const moduleSpec of expandedModules) {
    if (moduleSpec.kind === "protected") {
      const { entry, summary } = await writeSeededEncryptedModule({
        pluginRoot: resolvedPluginRoot,
        moduleSpec,
        encryptedBytes: Buffer.from(moduleSpec.encryptedBytes),
        keyHex: moduleSpec.keyHex,
        publication: moduleSpec.publication,
        providerConfig,
      });

      plugins = upsertCatalogEntry(plugins, entry);
      seeded.push({
        ...summary,
        slug: moduleSpec.slug,
        protectedModulePath: moduleSpec.protectedModulePath,
        exportName: moduleSpec.exportName,
        artifactIndex: moduleSpec.artifactIndex,
      });
      continue;
    }

    const [wasmBytes, manifest, keypair] = await Promise.all([
      fs.readFile(moduleSpec.wasmPath),
      loadModuleManifest(moduleSpec),
      generateX25519Keypair(),
    ]);
    const resolvedVersion = resolveModuleVersion(moduleSpec, manifest);
    const normalizedManifest = {
      ...manifest,
      pluginId: moduleSpec.moduleId,
      version: resolvedVersion,
    };
    const protectedArtifact = await protectModuleArtifact({
      manifest: normalizedManifest,
      wasmBytes,
      recipientPublicKeyHex: Buffer.from(keypair.publicKey).toString("hex"),
      singleFileBundle: true,
      artifactId: moduleSpec.slug,
      programId: moduleSpec.moduleId,
    });
    const encryptedBytes = Buffer.from(protectedArtifact.protectedArtifactBytes);
    const keyHex = Buffer.from(keypair.privateKey).toString("hex");
    const publication = extractPublicationRecordCollection(
      protectedArtifact.protectedArtifactBytes,
    );
    const { entry, summary } = await writeSeededEncryptedModule({
      pluginRoot: resolvedPluginRoot,
      moduleSpec: {
        ...moduleSpec,
        version: resolvedVersion,
      },
      encryptedBytes,
      keyHex,
      publication,
      providerConfig,
    });

    plugins = upsertCatalogEntry(plugins, entry);
    seeded.push({
      ...summary,
      slug: moduleSpec.slug,
      wasmPath: moduleSpec.wasmPath,
      manifestPath: moduleSpec.manifestPath,
    });
  }

  await fs.writeFile(
    catalogPath,
    `${JSON.stringify({ plugins }, null, 2)}\n`,
    { mode: 0o600 },
  );

  return {
    pluginRoot: resolvedPluginRoot,
    catalogPath,
    seeded,
  };
}

function resolveDefaultModules({ withConjunction = false } = {}) {
  return withConjunction
    ? [...DEFAULT_ORBPRO_MODULES, ...OPTIONAL_ORBPRO_MODULES]
    : [...DEFAULT_ORBPRO_MODULES];
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const pluginRoot = resolvePluginRoot(args);
  const summary = await seedOrbproModuleCatalog({
    pluginRoot,
    modules: resolveDefaultModules(args),
    providerX25519PubKey: args.providerX25519PubKey,
    providerPeerID: args.providerPeerID,
  });

  if (args.json) {
    process.stdout.write(`${JSON.stringify(summary, null, 2)}\n`);
    return;
  }

  console.log(`Seeded ${summary.seeded.length} OrbPro module artifact(s).`);
  console.log(`Plugin root: ${summary.pluginRoot}`);
  console.log(`Catalog: ${summary.catalogPath}`);
  for (const seeded of summary.seeded) {
    console.log(
      `- ${seeded.moduleId} -> ${seeded.encryptedPath} (${seeded.encryptedSizeBytes} bytes, sha256 ${seeded.bundleSHA256})`,
    );
    console.log(`  key envelope: ${seeded.keyEnvelopePath}`);
    console.log(`  allowed domains: ${seeded.allowedDomains.join(", ")}`);
  }
}

if (import.meta.url === pathToFileUrl(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}

function pathToFileUrl(filePath) {
  if (!filePath) {
    return new URL(import.meta.url);
  }
  return new URL(`file://${path.resolve(filePath)}`);
}

export {
  DEFAULT_ORBPRO_MODULES,
  OPTIONAL_ORBPRO_MODULES,
  defaultCacheControl,
  defaultContentType,
  defaultRequiredScope,
};
