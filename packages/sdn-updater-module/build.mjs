import { mkdir, readFile, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { gzipSync } from "node:zlib";

import { build } from "esbuild";
import { encodeAppManifest } from "space-data-module-sdk/app";
import { computeCanonicalModuleHash, createSingleFileBundle } from "space-data-module-sdk/bundle";
import { encodePluginManifest } from "space-data-module-sdk/manifest";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { compileModuleFromSource } from "space-data-module-sdk/compiler";

const packageRoot = dirname(fileURLToPath(import.meta.url));
const read = (...parts) => readFile(resolve(packageRoot, ...parts), "utf8");
const outputPath = resolve(packageRoot, "dist", "isomorphic", "module.wasm");
const manifest = JSON.parse(await read("manifest.json"));

// One translation unit: vendored Monocypher (its own #includes stripped), the
// BIP-39 wordlist, then the module.
const localInclude = /^#include "(monocypher|monocypher-ed25519|bip39_english\.inc)(\.h)?"\s*$/gm;
const sourceCode = [
  await read("third_party/monocypher/monocypher.h"),
  await read("third_party/monocypher/monocypher.c"),
  await read("third_party/monocypher/monocypher-ed25519.h"),
  "#undef FOR",
  await read("third_party/monocypher/monocypher-ed25519.c"),
  "#undef FOR",
  await read("src/bip39_english.inc"),
  await read("src/module.cpp"),
].join("\n").replace(localInclude, "");

await mkdir(dirname(outputPath), { recursive: true });
const result = await compileModuleFromSource({ manifest, sourceCode, language: "c++", outputPath, threadModel: "wasi-sequential" });
if (result.report && !result.report.ok) throw new Error(JSON.stringify(result.report.issues));

// The page the dashboard's module loader opens (dashboard data/module-app.js):
// one self-contained HTML document in the module's $APP record.
const canonical = await computeCanonicalModuleHash(await readFile(outputPath));
const bundled = await build({
  entryPoints: [resolve(packageRoot, "app", "updater.js")], bundle: true, write: false,
  platform: "browser", format: "esm", minify: true, target: "es2022",
  external: ["node:*", "fs", "path", "url", "module"],
  define: { __PLUGIN_MANIFEST__: JSON.stringify(manifest) },
});
const script = bundled.outputFiles[0].text.replaceAll("</script", "<\\/script");
const page = (await read("app", "index.html")).replace("__UPDATER_SCRIPT__", () => script);
const app = encodeAppManifest({
  id: "updater", name: "Updater", version: manifest.version, description: manifest.description,
  modules: [{ id: "updater", pluginId: manifest.pluginId, contentHash: canonical.hashHex, version: manifest.version, role: "primary", runtimeTarget: "both" }],
  data: [],
  pages: [{ id: "updater", title: "Updater", mediaType: "text/html", entry: true, encoding: "base64_gzip",
    content: gzipSync(page, { level: 9 }).toString("base64"), contentSha256: createHash("sha256").update(page).digest("hex") }],
});
const bundle = await createSingleFileBundle({
  wasmBytes: canonical.canonicalWasmBytes, manifestBytes: encodePluginManifest(manifest),
  entries: [{ entryId: "app.app", role: "auxiliary", sectionName: "sdn.app.record", payloadEncoding: "flatbuffer", typeRef: { schemaName: "APP.fbs", fileIdentifier: "$APP" }, payload: app }],
});
await writeFile(outputPath, bundle.wasmBytes);
console.log(`built ${bundle.wasmBytes.length} bytes at ${outputPath} (${result.compiler}, ${result.threadModel}); module ${canonical.hashHex}; page ${page.length} bytes`);
