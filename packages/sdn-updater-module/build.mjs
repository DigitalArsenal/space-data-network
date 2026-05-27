import { mkdir, readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { compileModuleFromSource } from "space-data-module-sdk/compiler";

const packageRoot = dirname(fileURLToPath(import.meta.url));
const manifestPath = resolve(packageRoot, "manifest.json");
const sourcePath = resolve(packageRoot, "src", "module.cpp");
const outputPath = resolve(packageRoot, "dist", "isomorphic", "module.wasm");

const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
const sourceCode = await readFile(sourcePath, "utf8");

await mkdir(dirname(outputPath), { recursive: true });

const result = await compileModuleFromSource({
  manifest,
  sourceCode,
  language: "c++",
  outputPath,
});

console.log(
  `built ${result.wasmBytes.length} bytes at ${result.outputPath} ` +
    `(${result.compiler}, ${result.threadModel})`,
);
