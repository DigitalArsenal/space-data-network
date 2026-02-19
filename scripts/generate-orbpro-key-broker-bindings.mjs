#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { FlatcRunner } from "flatc-wasm";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, "..");
const pluginSdkRoot = path.join(repoRoot, "packages/plugin-sdk");

const schemaFiles = [
  "PublicKeyResponse.fbs",
  "KeyBrokerRequest.fbs",
  "KeyBrokerResponse.fbs",
];

const schemaDir = path.join(pluginSdkRoot, "schemas/orbpro/key-broker");
const pluginSdkGenerator = path.join(
  pluginSdkRoot,
  "scripts/generate-key-broker-bindings.mjs",
);
const goOutDir = path.join(
  repoRoot,
  "sdn-server/internal/wasiplugin/fbs/orbpro/keybroker",
);

function virtualPath(fileName) {
  return `/schemas/orbpro/key-broker/${fileName}`;
}

function ensurePathExists(target, label) {
  return fs.access(target).catch(() => {
    throw new Error(`Missing ${label}: ${target}`);
  });
}

function runPluginSdkGenerator() {
  const result = spawnSync(process.execPath, [pluginSdkGenerator], {
    cwd: repoRoot,
    stdio: "inherit",
  });
  if (result.status !== 0) {
    throw new Error(
      `Plugin SDK binding generation failed with exit code ${result.status ?? 1}`,
    );
  }
}

async function loadSchemaTree() {
  const files = {};
  for (const fileName of schemaFiles) {
    const content = await fs.readFile(path.join(schemaDir, fileName), "utf8");
    files[virtualPath(fileName)] = content;
  }
  return files;
}

function collectGoBindings(generated) {
  const out = new Map();
  for (const [relativePath, content] of Object.entries(generated)) {
    if (
      relativePath.startsWith("orbpro/keybroker/") &&
      relativePath.endsWith(".go")
    ) {
      out.set(path.basename(relativePath), content);
    }
  }
  return out;
}

async function main() {
  await ensurePathExists(pluginSdkRoot, "plugin-sdk package directory");
  await ensurePathExists(schemaDir, "plugin-sdk key-broker schema directory");
  await ensurePathExists(pluginSdkGenerator, "plugin-sdk generator script");

  runPluginSdkGenerator();

  const schemaTree = await loadSchemaTree();
  const flatc = await FlatcRunner.init();
  const goBindings = new Map();

  for (const entryFile of schemaFiles) {
    const generated = flatc.generateCode(
      {
        entry: virtualPath(entryFile),
        files: schemaTree,
      },
      "go",
    );

    for (const [fileName, content] of collectGoBindings(generated)) {
      goBindings.set(fileName, content);
    }
  }

  await fs.mkdir(goOutDir, { recursive: true });
  for (const [fileName, content] of goBindings.entries()) {
    await fs.writeFile(path.join(goOutDir, fileName), content, "utf8");
  }

  console.log(
    `Generated ${goBindings.size} SDN Go bindings -> ${path.relative(repoRoot, goOutDir)}`,
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
