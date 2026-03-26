import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { FlatcRunner } from "flatc-wasm";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, "..", "..", "..");

const goOutDir = path.join(
  repoRoot,
  "packages/space-data-network/sdn-server/internal/wasiplugin/fbs",
);

const schemaSpecs = [
  {
    root: path.join(repoRoot, "packages/space-data-module-sdk/schemas"),
    files: [
      "PluginInvokeRequest.fbs",
      "PluginInvokeResponse.fbs",
      "TypedArenaBuffer.fbs",
    ],
  },
  {
    root: path.join(
      repoRoot,
      "packages/space-data-network/sdn-server/internal/wasiplugin/fbs/schemas",
    ),
    files: ["RawDataPayload.fbs"],
  },
];

async function loadSchemaTree() {
  const files = {};
  for (const spec of schemaSpecs) {
    for (const fileName of spec.files) {
      const content = await fs.readFile(path.join(spec.root, fileName), "utf8");
      files[`/schemas/${fileName}`] = content;
    }
  }
  return files;
}

async function writeGeneratedGoFiles(generated) {
  for (const [relativePath, content] of Object.entries(generated)) {
    if (!relativePath.endsWith(".go")) {
      continue;
    }
    if (
      !relativePath.startsWith("orbpro/invoke/") &&
      !relativePath.startsWith("orbpro/stream/") &&
      !relativePath.startsWith("orbpro/plugin/")
    ) {
      continue;
    }
    const outPath = path.join(goOutDir, relativePath);
    const normalizedContent = content.replace(
      /"orbpro\/stream"/g,
      '"github.com/spacedatanetwork/sdn-server/internal/wasiplugin/fbs/orbpro/stream"',
    );
    await fs.mkdir(path.dirname(outPath), { recursive: true });
    await fs.writeFile(outPath, normalizedContent, "utf8");
  }
}

async function main() {
  const schemaTree = await loadSchemaTree();
  const flatc = await FlatcRunner.init();
  const generated = {};

  for (const spec of schemaSpecs) {
    for (const fileName of spec.files) {
      const result = flatc.generateCode(
        {
          entry: `/schemas/${fileName}`,
          files: schemaTree,
        },
        "go",
      );
      Object.assign(generated, result);
    }
  }

  await writeGeneratedGoFiles(generated);
  console.log(
    `Generated module SDK Go bindings -> ${path.relative(repoRoot, goOutDir)}`,
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
