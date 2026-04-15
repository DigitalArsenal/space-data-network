#!/usr/bin/env node

import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import viewshedShaderPackageJson from "../../space-data-network-plugins/packages/viewshed-shader/package.json" with { type: "json" };
import sensorShadersPackageJson from "../../space-data-network-plugins/packages/sensor-shaders/package.json" with { type: "json" };
import sgp4PackageJson from "../../space-data-network-plugins/packages/sgp4/package.json" with { type: "json" };
import fastestPathPackageJson from "../../space-data-network-plugins/packages/fastest-path/package.json" with { type: "json" };
import hpopPackageJson from "../../space-data-network-plugins/packages/hpop/package.json" with { type: "json" };
import conjunctionAssessmentPackageJson from "../../space-data-network-plugins/packages/conjunction-assessment/package.json" with { type: "json" };

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(__dirname, "..");
const workspaceRoot = path.resolve(packageRoot, "..", "..");
const defaultCacheControl =
  "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400";
const defaultContentType = "application/wasm+encrypted";
const defaultRequiredScope = "orbpro:base";
const defaultVersion = "local-dev";

const DEFAULT_ORBPRO_MODULES = Object.freeze([
  Object.freeze({
    slug: "viewshed-shader",
    moduleId: "com.orbpro.viewshed-shader",
    version: viewshedShaderPackageJson.version,
    wasmPath:
      "packages/space-data-network-plugins/packages/viewshed-shader/dist/viewshed-shader.wasm",
  }),
  Object.freeze({
    slug: "sensor-shaders",
    moduleId: "com.orbpro.sensor-shaders",
    version: sensorShadersPackageJson.version,
    wasmPath:
      "packages/space-data-network-plugins/packages/sensor-shaders/dist/isomorphic/module.wasm",
  }),
  Object.freeze({
    slug: "sgp4",
    moduleId: "com.orbpro.sgp4",
    version: sgp4PackageJson.version,
    wasmPath: "packages/space-data-network-plugins/packages/sgp4/dist/sgp4.wasm",
  }),
  Object.freeze({
    slug: "fastest-path",
    moduleId: "com.orbpro.fastest-path",
    version: fastestPathPackageJson.version,
    wasmPath:
      "packages/space-data-network-plugins/packages/fastest-path/dist/isomorphic/module.wasm",
  }),
  Object.freeze({
    slug: "hpop",
    moduleId: "com.orbpro.hpop",
    version: hpopPackageJson.version,
    wasmPath:
      "packages/space-data-network-plugins/packages/hpop/dist/isomorphic/module.wasm",
  }),
]);

const OPTIONAL_ORBPRO_MODULES = Object.freeze([
  Object.freeze({
    slug: "conjunction-assessment",
    moduleId: "org.spacedata.analysis.conjunction.assessment",
    version: conjunctionAssessmentPackageJson.version,
    wasmPath:
      "packages/space-data-network-plugins/packages/conjunction-assessment/dist/isomorphic/module.wasm",
  }),
]);

const BUILT_IN_MODULE_VERSIONS = Object.freeze(
  new Map([
    ["viewshed-shader", viewshedShaderPackageJson.version],
    ["com.orbpro.viewshed-shader", viewshedShaderPackageJson.version],
    ["sensor-shaders", sensorShadersPackageJson.version],
    ["com.orbpro.sensor-shaders", sensorShadersPackageJson.version],
    ["sgp4", sgp4PackageJson.version],
    ["com.orbpro.sgp4", sgp4PackageJson.version],
    ["fastest-path", fastestPathPackageJson.version],
    ["com.orbpro.fastest-path", fastestPathPackageJson.version],
    ["hpop", hpopPackageJson.version],
    ["com.orbpro.hpop", hpopPackageJson.version],
    [
      "conjunction-assessment",
      conjunctionAssessmentPackageJson.version,
    ],
    [
      "org.spacedata.analysis.conjunction.assessment",
      conjunctionAssessmentPackageJson.version,
    ],
  ]),
);

function resolveModuleVersion(moduleSpec, slug, moduleId) {
  const explicitVersion = String(moduleSpec?.version || "").trim();
  if (explicitVersion) {
    return explicitVersion;
  }

  return (
    BUILT_IN_MODULE_VERSIONS.get(moduleId) ??
    BUILT_IN_MODULE_VERSIONS.get(slug) ??
    defaultVersion
  );
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

export function encryptBundleBytes(plaintext, contentKey) {
  const iv = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv("aes-256-gcm", contentKey, iv);
  const ciphertext = Buffer.concat([
    cipher.update(Buffer.from(plaintext)),
    cipher.final(),
  ]);
  const tag = cipher.getAuthTag();
  return Buffer.concat([iv, ciphertext, tag]);
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
  return path.isAbsolute(rawPath)
    ? rawPath
    : path.resolve(workspaceRoot, rawPath);
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
    version: resolveModuleVersion(moduleSpec, slug, moduleId),
    wasmPath: resolveModulePath(moduleSpec?.wasmPath),
  };
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
    const bundleBytes = await fs.readFile(moduleSpec.wasmPath);
    const contentKey = crypto.randomBytes(32);
    const encryptedBytes = encryptBundleBytes(bundleBytes, contentKey);
    const entry = buildCatalogEntry(moduleSpec);
    const encryptedPath = path.join(
      resolvedPluginRoot,
      entry.encrypted_path,
    );
    const keyPath = path.join(resolvedPluginRoot, entry.key_path);

    await fs.writeFile(encryptedPath, encryptedBytes, { mode: 0o600 });
    await fs.writeFile(keyPath, contentKey.toString("hex"), { mode: 0o600 });

    plugins = upsertCatalogEntry(plugins, entry);
    seeded.push({
      slug: moduleSpec.slug,
      moduleId: moduleSpec.moduleId,
      version: entry.version,
      wasmPath: moduleSpec.wasmPath,
      encryptedPath,
      keyPath,
      encryptedSizeBytes: encryptedBytes.length,
      contentKeyHex: contentKey.toString("hex"),
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
