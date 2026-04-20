#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
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

const DEFAULT_ORBPRO_MODULES = Object.freeze([
  Object.freeze({
    slug: "licensing",
    moduleId: "licensing",
    wasmPath:
      "../space-data-network-plugins/packages/licensing/dist/isomorphic/module.wasm",
    manifestPath:
      "../space-data-network-plugins/packages/licensing/plugin-manifest.json",
    requiredScope: "orbpro:runtime",
  }),
  Object.freeze({
    slug: "viewshed-shader",
    moduleId: "com.orbpro.viewshed-shader",
    wasmPath:
      "../space-data-network-plugins/packages/viewshed-shader/dist/viewshed-shader.wasm",
    manifestPath:
      "../space-data-network-plugins/packages/viewshed-shader/dist/manifest.json",
  }),
  Object.freeze({
    slug: "sensor-shaders",
    moduleId: "com.orbpro.sensor-shaders",
    wasmPath:
      "../space-data-network-plugins/packages/sensor-shaders/dist/isomorphic/module.wasm",
    manifestPath:
      "../space-data-network-plugins/packages/sensor-shaders/plugin-manifest.json",
  }),
  Object.freeze({
    slug: "sgp4",
    moduleId: "com.orbpro.sgp4",
    wasmPath: "../space-data-network-plugins/packages/sgp4/dist/sgp4.wasm",
    manifestPath: "../space-data-network-plugins/packages/sgp4/dist/manifest.json",
  }),
  Object.freeze({
    slug: "fastest-path",
    moduleId: "com.orbpro.fastest-path",
    wasmPath:
      "../space-data-network-plugins/packages/fastest-path/dist/isomorphic/module.wasm",
    manifestPath:
      "../space-data-network-plugins/packages/fastest-path/plugin-manifest.json",
  }),
  Object.freeze({
    slug: "hpop",
    moduleId: "com.orbpro.hpop",
    wasmPath:
      "../space-data-network-plugins/packages/hpop/dist/isomorphic/module.wasm",
    manifestPath: "../space-data-network-plugins/packages/hpop/plugin-manifest.json",
  }),
]);

const OPTIONAL_ORBPRO_MODULES = Object.freeze([
  Object.freeze({
    slug: "conjunction-assessment",
    moduleId: "org.spacedata.analysis.conjunction.assessment",
    wasmPath:
      "../space-data-network-plugins/packages/conjunction-assessment/dist/isomorphic/module.wasm",
    manifestPath:
      "../space-data-network-plugins/packages/conjunction-assessment/plugin-manifest.json",
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

function resolveModulePath(relativeOrAbsolutePath) {
  const rawPath = String(relativeOrAbsolutePath || "").trim();
  if (!rawPath) {
    throw new Error("Module wasmPath is required.");
  }
  if (path.isAbsolute(rawPath)) {
    return rawPath;
  }
  const candidatePaths = [
    path.resolve(repoRoot, rawPath),
    path.resolve(workspaceRoot, rawPath),
  ];
  return candidatePaths[0];
}

function resolveManifestPath(moduleSpec) {
  const explicitPath = String(moduleSpec?.manifestPath || "").trim();
  if (explicitPath) {
    return resolveModulePath(explicitPath);
  }

  const wasmPath = resolveModulePath(moduleSpec?.wasmPath);
  const candidatePaths = [
    path.resolve(path.dirname(wasmPath), "manifest.json"),
    path.resolve(path.dirname(wasmPath), "..", "manifest.json"),
    path.resolve(path.dirname(wasmPath), "..", "plugin-manifest.json"),
    path.resolve(path.dirname(wasmPath), "..", "..", "plugin-manifest.json"),
  ];
  return candidatePaths[0];
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
    wasmPath: resolveModulePath(moduleSpec?.wasmPath),
    manifestPath: resolveManifestPath(moduleSpec),
  };
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
  let plugins = [...existingPlugins];
  const seeded = [];

  for (const rawModule of modules) {
    const moduleSpec = normalizeModuleSpec(rawModule);
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
