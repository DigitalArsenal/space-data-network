#!/usr/bin/env node
// Generate node-internal publication bindings with the pinned wasm compiler.
import { promises as fs } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { FlatcRunner } from "flatc-wasm";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const base = path.join(root, "sdn-server/internal/publicationstate");
const flatc = await FlatcRunner.init();
flatc.Module.FS.mkdirTree("/schema");
flatc.Module.FS.mkdirTree("/out");
// Each schema and the Go package (its namespace) it generates.
for (const [schema, pkg] of [["PublicationState.fbs", "pst"], ["PublicationMark.fbs", "pmk"]]) {
  flatc.Module.FS.writeFile(`/schema/${schema}`, await fs.readFile(path.join(base, "schema", schema), "utf8"));
  const result = flatc.runCommand(["--preserve-case", "--go", "--gen-object-api", "-o", "/out", `/schema/${schema}`]);
  if (result.code !== 0 || (result.stderr || "").includes("error:")) {
    throw new Error(result.stderr || result.stdout);
  }
  await fs.mkdir(path.join(base, pkg), { recursive: true });
  for (const file of flatc.Module.FS.readdir(`/out/${pkg}`)) {
    if (!file.endsWith(".go")) continue;
    const source = flatc.Module.FS.readFile(`/out/${pkg}/${file}`, { encoding: "utf8" });
    await fs.writeFile(path.join(base, pkg, file), source.replace(/[ \t]+$/gm, "").replace(/\n+$/u, "\n"));
  }
}
console.log("Generated publication state Go bindings.");
