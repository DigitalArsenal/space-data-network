#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { FlatcRunner } from "flatc-wasm";
import { transform } from "esbuild";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");

const schemaFiles = [
  "ThirdPartyClientLicenseRequest.fbs",
  "ThirdPartyClientLicenseResponse.fbs",
  "ThirdPartyServerPluginRegistration.fbs",
  "ThirdPartyServerPluginGrant.fbs",
];
const schemaRoot = path.join(packageRoot, "schemas/orbpro/third-party/v1");
const tsOutDir = path.join(packageRoot, "src/generated/orbpro/thirdparty/v1");
const goOutDir = path.join(packageRoot, "src/generated-go/orbpro/thirdparty/v1");

function asVirtualSchemaPath(fileName) {
  return `/schemas/orbpro/third-party/v1/${fileName}`;
}

function addJsImportExtensions(code) {
  return code.replace(
    /((?:import|export)\s+[^'"]*?\sfrom\s+)(['"])(\.[^'"]*?)(\2)/g,
    (match, prefix, quote, specifier, suffix) => {
      if (/\.[cm]?js$/.test(specifier) || /\.json$/.test(specifier)) {
        return match;
      }
      return `${prefix}${quote}${specifier}.js${suffix}`;
    },
  );
}

function parseArgs(argv) {
  const out = {
    targets: new Set(["ts", "go"]),
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];

    if (arg === "--targets" && next) {
      const targets = next
        .split(",")
        .map((entry) => entry.trim().toLowerCase())
        .filter(Boolean);
      out.targets = new Set(targets);
      i++;
      continue;
    }

    if (arg === "--help" || arg === "-h") {
      printUsage();
      process.exit(0);
    }
  }

  if (!out.targets.has("ts") && !out.targets.has("go")) {
    throw new Error("--targets must include at least one of ts,go");
  }

  return out;
}

function printUsage() {
  console.log(`Usage:\n  node scripts/generate-third-party-bindings.mjs [options]\n\nOptions:\n  --targets ts,go   Output bindings to generate (default: ts,go)\n  --help            Show this help\n`);
}

async function loadSchemaTree() {
  const files = {};
  for (const fileName of schemaFiles) {
    const content = await fs.readFile(path.join(schemaRoot, fileName), "utf8");
    files[asVirtualSchemaPath(fileName)] = content;
  }
  return files;
}

function collectBindings(generated, extension, prefixPath) {
  const out = new Map();
  for (const [relativePath, content] of Object.entries(generated)) {
    if (relativePath.startsWith(prefixPath) && relativePath.endsWith(extension)) {
      out.set(path.basename(relativePath), content);
    }
  }
  return out;
}

async function writeTsBindings(tsBindings) {
  await fs.mkdir(tsOutDir, { recursive: true });
  for (const [tsFileName, tsSource] of tsBindings.entries()) {
    const tsPath = path.join(tsOutDir, tsFileName);
    const jsFileName = tsFileName.replace(/\.ts$/, ".js");
    const jsPath = path.join(tsOutDir, jsFileName);

    await fs.writeFile(tsPath, tsSource, "utf8");

    const transformed = await transform(tsSource, {
      loader: "ts",
      format: "esm",
      target: "es2020",
    });
    const jsSource = addJsImportExtensions(transformed.code);
    await fs.writeFile(jsPath, jsSource, "utf8");
  }
}

async function writeGoBindings(goBindings) {
  await fs.mkdir(goOutDir, { recursive: true });
  for (const [fileName, content] of goBindings.entries()) {
    await fs.writeFile(path.join(goOutDir, fileName), content, "utf8");
  }
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const schemaTree = await loadSchemaTree();
  const flatc = await FlatcRunner.init();

  const tsBindings = new Map();
  const goBindings = new Map();

  for (const entryFile of schemaFiles) {
    const schemaInput = {
      entry: asVirtualSchemaPath(entryFile),
      files: schemaTree,
    };

    if (args.targets.has("ts")) {
      const generatedTs = flatc.generateCode(schemaInput, "ts");
      for (const [fileName, content] of collectBindings(
        generatedTs,
        ".ts",
        "orbpro/thirdparty/v1/",
      )) {
        tsBindings.set(fileName, content);
      }
    }

    if (args.targets.has("go")) {
      const generatedGo = flatc.generateCode(schemaInput, "go");
      for (const [fileName, content] of collectBindings(
        generatedGo,
        ".go",
        "orbpro/thirdparty/v1/",
      )) {
        goBindings.set(fileName, content);
      }
    }
  }

  if (args.targets.has("ts")) {
    await writeTsBindings(tsBindings);
    console.log(
      `Generated ${tsBindings.size} third-party TS+JS bindings -> ${path.relative(packageRoot, tsOutDir)}`,
    );
  }

  if (args.targets.has("go")) {
    await writeGoBindings(goBindings);
    console.log(
      `Generated ${goBindings.size} third-party Go bindings -> ${path.relative(packageRoot, goOutDir)}`,
    );
  }
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
