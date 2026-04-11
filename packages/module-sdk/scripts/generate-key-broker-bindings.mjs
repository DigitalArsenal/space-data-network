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
  "PublicKeyResponse.fbs",
  "KeyBrokerRequest.fbs",
  "KeyBrokerResponse.fbs",
];
const schemaRoot = path.join(packageRoot, "schemas/orbpro/key-broker");
const outDir = path.join(packageRoot, "src/generated/orbpro/keybroker");

function asVirtualSchemaPath(fileName) {
  return `/schemas/orbpro/key-broker/${fileName}`;
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

async function loadSchemaTree() {
  const files = {};
  for (const fileName of schemaFiles) {
    const content = await fs.readFile(path.join(schemaRoot, fileName), "utf8");
    files[asVirtualSchemaPath(fileName)] = content;
  }
  return files;
}

function collectTsBindings(generated) {
  const out = new Map();
  for (const [relativePath, content] of Object.entries(generated)) {
    if (
      relativePath.startsWith("orbpro/keybroker/") &&
      relativePath.endsWith(".ts")
    ) {
      out.set(path.basename(relativePath), content);
    }
  }
  return out;
}

async function writeBindings(tsBindings) {
  await fs.mkdir(outDir, { recursive: true });
  for (const [tsFileName, tsSource] of tsBindings.entries()) {
    const tsPath = path.join(outDir, tsFileName);
    const jsFileName = tsFileName.replace(/\.ts$/, ".js");
    const jsPath = path.join(outDir, jsFileName);

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

async function main() {
  const schemaTree = await loadSchemaTree();
  const flatc = await FlatcRunner.init();
  const tsBindings = new Map();

  for (const entryFile of schemaFiles) {
    const generatedTs = flatc.generateCode(
      {
        entry: asVirtualSchemaPath(entryFile),
        files: schemaTree,
      },
      "ts",
    );
    for (const [fileName, content] of collectTsBindings(generatedTs)) {
      tsBindings.set(fileName, content);
    }
  }

  await writeBindings(tsBindings);
  console.log(
    `Generated ${tsBindings.size} TS+JS bindings -> ${path.relative(packageRoot, outDir)}`,
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
