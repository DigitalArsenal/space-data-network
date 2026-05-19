#!/usr/bin/env node

import fsSync from "node:fs";
import fs from "node:fs/promises";
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
const currentSpaceDataNetworkModulesVersion = "0.1.0-0.8.2";
const orbproProtectedContextPrefix = "orbpro.plugin/";
const moduleIdPattern = /^[A-Za-z0-9._-]+$/;
const staleManagedModuleIds = Object.freeze([
  "licensing",
  "conjunction-assessment",
  "com.orbpro.wasm-engine-sdk",
]);

const DEFAULT_ORBPRO_MODULES = Object.freeze([
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
    slug: "wasm-engine",
    protectedModulePath:
      "packages/wasm-engine/dist/wasm-engine-sdn-encrypted.js",
    protectedExports: Object.freeze([
      Object.freeze({ exportName: "encryptedData", slug: "wasm-engine" }),
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
  Object.freeze({
    slug: "licensing",
    moduleId: "licensing",
    wasmPath:
      "packages/space-data-network-modules/licensing/core/dist/isomorphic/module.wasm",
    manifestPath:
      "packages/space-data-network-modules/licensing/core/plugin-manifest.json",
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
  --plugin-root <path>      Exact plugin root (catalog.json lives here)
  --storage-path <path>     Storage root; helper writes to <storage>/license/plugins
  --with-conjunction        Include the standalone conjunction plugin
  --json                    Print the final summary JSON only
  --help                    Show this message
`);
}

function parseArgs(argv) {
  const options = {
    pluginRoot: "",
    storagePath: "",
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
    path.resolve(packageRoot, "..", rawPath.replace(/^packages[\\/]/, "")),
    path.resolve(packageRoot, "..", "OrbPro", rawPath),
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
  return {
    id: moduleSpec.moduleId,
    version: moduleSpec.version || defaultVersion,
    required_scope: moduleSpec.requiredScope || defaultRequiredScope,
    encrypted_path: `${moduleSpec.slug}.wasm.enc`,
    key_path: `${moduleSpec.slug}.key`,
    content_type: moduleSpec.contentType || defaultContentType,
    cache_control: moduleSpec.cacheControl || defaultCacheControl,
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
  const normalized = String(value || "")
    .trim()
    .toLowerCase();
  if (!/^[0-9a-f]{64}$/.test(normalized)) {
    throw new Error(`${context} must export a 32-byte X25519 private key hex string.`);
  }
  return normalized;
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
} = {}) {
  const resolvedPluginRoot = resolvePluginRoot({ pluginRoot, storagePath });
  await fs.mkdir(resolvedPluginRoot, { recursive: true });

  const { catalogPath, plugins: existingPlugins } = await readCatalog(
    resolvedPluginRoot,
  );
  const seeded = [];
  const expandedModules = await expandModuleSpecs(modules);
  const managedModuleIds = new Set([
    ...staleManagedModuleIds,
    ...expandedModules.map((moduleSpec) => moduleSpec.moduleId),
  ]);
  let plugins = existingPlugins.filter(
    (entry) => !managedModuleIds.has(String(entry?.id || "").trim()),
  );

  for (const moduleSpec of expandedModules) {
    if (moduleSpec.kind === "protected") {
      const entry = buildCatalogEntry(moduleSpec);
      const encryptedPath = path.join(
        resolvedPluginRoot,
        entry.encrypted_path,
      );
      const keyPath = path.join(resolvedPluginRoot, entry.key_path);

      await fs.writeFile(encryptedPath, moduleSpec.encryptedBytes, {
        mode: 0o600,
      });
      await fs.writeFile(keyPath, moduleSpec.keyHex, { mode: 0o600 });

      plugins = upsertCatalogEntry(plugins, entry);
      seeded.push({
        slug: moduleSpec.slug,
        moduleId: moduleSpec.moduleId,
        version: entry.version,
        protectedModulePath: moduleSpec.protectedModulePath,
        exportName: moduleSpec.exportName,
        artifactIndex: moduleSpec.artifactIndex,
        encryptedPath,
        keyPath,
        encryptedSizeBytes: moduleSpec.encryptedBytes.length,
        contentKeyHex: moduleSpec.keyHex,
        hasMbl: Boolean(moduleSpec.publication?.mbl),
        hasEnc: Boolean(moduleSpec.publication?.enc),
        hasPnm: Boolean(moduleSpec.publication?.pnm),
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
    const entry = buildCatalogEntry({
      ...moduleSpec,
      version: resolvedVersion,
    });
    const encryptedPath = path.join(
      resolvedPluginRoot,
      entry.encrypted_path,
    );
    const keyPath = path.join(resolvedPluginRoot, entry.key_path);

    await fs.writeFile(encryptedPath, encryptedBytes, { mode: 0o600 });
    await fs.writeFile(keyPath, keyHex, { mode: 0o600 });

    plugins = upsertCatalogEntry(plugins, entry);
    seeded.push({
      slug: moduleSpec.slug,
      moduleId: moduleSpec.moduleId,
      version: entry.version,
      wasmPath: moduleSpec.wasmPath,
      manifestPath: moduleSpec.manifestPath,
      encryptedPath,
      keyPath,
      encryptedSizeBytes: encryptedBytes.length,
      contentKeyHex: keyHex,
      hasMbl: Boolean(publication?.mbl),
      hasEnc: Boolean(publication?.enc),
      hasPnm: Boolean(publication?.pnm),
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
      `- ${seeded.moduleId} -> ${seeded.encryptedPath} (${seeded.encryptedSizeBytes} bytes)`,
    );
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
