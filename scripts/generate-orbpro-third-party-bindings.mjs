#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const repoRoot = path.resolve(__dirname, "..");
const pluginSdkRoot = path.join(repoRoot, "packages/plugin-sdk");
const pluginSdkGenerator = path.join(
  pluginSdkRoot,
  "scripts/generate-third-party-bindings.mjs",
);

const generatedGoDir = path.join(
  pluginSdkRoot,
  "src/generated-go/orbpro/thirdparty/v1",
);
const sdnServerGoOutDir = path.join(
  repoRoot,
  "sdn-server/internal/wasiplugin/fbs/orbpro/thirdparty/v1",
);

function runOrThrow(command, args) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    stdio: "inherit",
  });

  if (result.status !== 0) {
    throw new Error(
      `Command failed (${result.status ?? 1}): ${command} ${args.join(" ")}`,
    );
  }
}

async function copyDirContents(sourceDir, targetDir) {
  await fs.mkdir(targetDir, { recursive: true });
  const entries = await fs.readdir(sourceDir, { withFileTypes: true });

  for (const entry of entries) {
    if (!entry.isFile()) {
      continue;
    }
    const sourcePath = path.join(sourceDir, entry.name);
    const targetPath = path.join(targetDir, entry.name);
    await fs.copyFile(sourcePath, targetPath);
  }
}

async function main() {
  await fs.access(pluginSdkRoot);
  await fs.access(pluginSdkGenerator);

  runOrThrow(process.execPath, [pluginSdkGenerator, "--targets", "ts,go"]);
  await copyDirContents(generatedGoDir, sdnServerGoOutDir);

  console.log(
    `Generated OrbPro third-party bindings and staged Go outputs -> ${path.relative(repoRoot, sdnServerGoOutDir)}`,
  );
}

main().catch((error) => {
  console.error(error.message || error);
  process.exit(1);
});
