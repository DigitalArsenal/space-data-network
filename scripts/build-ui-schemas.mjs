#!/usr/bin/env node
// build-ui-schemas.mjs — compile LOCAL UI-transport FlatBuffers schemas with
// the wasm flatc (flatc-wasm), never a native binary (Janus ruling 2026-07-24:
// the version-pinned wasm compiler is the only generator in this stack).
//
// Sources:  sdn-server/internal/status/schema/*.fbs
// Outputs:  Go → sdn-server/internal/status/nst/   (package nst, checked in)
//           TS → sdn-js/src/status/generated/      (checked in)
//
// Deterministic: outputs are whitespace-normalized the same way
// spacedatastandards.org/scripts/generateSource.mjs does.

import { promises as fs } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { FlatcRunner } from "flatc-wasm";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const SCHEMA_DIR = path.join(ROOT, "sdn-server/internal/status/schema");
const GO_OUT = path.join(ROOT, "sdn-server/internal/status");
const TS_OUT = path.join(ROOT, "sdn-js/src/status/generated");

function collectOutputs(flatc, directoryPath) {
  const outputs = new Map();
  const walk = (currentPath, relativeBase = "") => {
    for (const entry of flatc.Module.FS.readdir(currentPath)) {
      if (entry === "." || entry === "..") continue;
      const fullPath = `${currentPath}/${entry}`;
      const relativePath = relativeBase ? `${relativeBase}/${entry}` : entry;
      const stat = flatc.Module.FS.stat(fullPath);
      if (flatc.Module.FS.isDir(stat.mode)) {
        walk(fullPath, relativePath);
      } else {
        outputs.set(
          relativePath,
          flatc.Module.FS.readFile(fullPath, { encoding: "utf8" }),
        );
      }
    }
  };
  walk(directoryPath);
  return outputs;
}

async function writeOutputs(baseDir, outputs) {
  for (const [relativePath, source] of outputs.entries()) {
    const outputPath = path.join(baseDir, relativePath);
    const normalized = source.replace(/[ \t]+$/gm, "").replace(/\n+$/u, "\n");
    await fs.mkdir(path.dirname(outputPath), { recursive: true });
    await fs.writeFile(outputPath, normalized, "utf8");
  }
}

function generate(flatc, lang, outVirtualDir, entries) {
  flatc.Module.FS.mkdirTree(outVirtualDir);
  const result = flatc.runCommand([
    "--preserve-case",
    lang,
    "-o",
    outVirtualDir,
    ...entries,
  ]);
  if (result.code !== 0 || (result.stderr || "").includes("error:")) {
    throw new Error(
      `flatc ${lang} failed:\n${result.stderr || result.stdout}`,
    );
  }
  return collectOutputs(flatc, outVirtualDir);
}

const schemaFiles = (await fs.readdir(SCHEMA_DIR)).filter((f) =>
  f.endsWith(".fbs"),
);
if (schemaFiles.length === 0) {
  console.error(`no .fbs schemas found in ${SCHEMA_DIR}`);
  process.exit(1);
}

const flatc = await FlatcRunner.init();
flatc.Module.FS.mkdirTree("/schema");
const entries = [];
for (const f of schemaFiles) {
  const text = await fs.readFile(path.join(SCHEMA_DIR, f), "utf8");
  const virtual = `/schema/${f}`;
  flatc.Module.FS.writeFile(virtual, text);
  entries.push(virtual);
}

const goOutputs = generate(flatc, "--go", "/out_go", entries);
const tsOutputs = generate(flatc, "--ts", "/out_ts", entries);
await writeOutputs(GO_OUT, goOutputs);
await writeOutputs(TS_OUT, tsOutputs);

console.log(
  `ui-schemas: ${schemaFiles.length} schema(s) → ${goOutputs.size} Go file(s) in ${path.relative(ROOT, GO_OUT)}, ${tsOutputs.size} TS file(s) in ${path.relative(ROOT, TS_OUT)}`,
);
