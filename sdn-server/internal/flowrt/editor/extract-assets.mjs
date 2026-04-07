#!/usr/bin/env node
// Extracts editor static assets from sdn-flow's embeddedAssets.generated.js
// into a dist/ directory for Go embedding.
//
// Usage: node extract-assets.mjs <path-to-sdn-flow>
//
// Output: dist/ directory with all static files ready for //go:embed

import { readFileSync, writeFileSync, mkdirSync, existsSync } from "fs";
import { join, dirname } from "path";
import { fileURLToPath } from "url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const sdnFlowPath = process.argv[2] || join(__dirname, "../../../../..", "sdn-flow");
const distDir = join(__dirname, "dist");

async function main() {
  const assetsPath = join(sdnFlowPath, "src/editor/embeddedAssets.generated.js");
  if (!existsSync(assetsPath)) {
    console.error(`Assets file not found: ${assetsPath}`);
    console.error(`Run 'npm run build:editor-assets' in sdn-flow first.`);
    process.exit(1);
  }

  // Dynamic import to get the exported object
  const mod = await import(assetsPath);
  const assets = mod.EmbeddedEditorAssets;

  if (!assets || typeof assets !== "object") {
    console.error("Failed to read EmbeddedEditorAssets");
    process.exit(1);
  }

  // Clean and create dist
  mkdirSync(distDir, { recursive: true });

  let count = 0;
  for (const [route, entry] of Object.entries(assets)) {
    // Normalize route to file path
    let filePath = route;
    if (filePath === "/") filePath = "/index.html";
    if (filePath.startsWith("/")) filePath = filePath.slice(1);

    const fullPath = join(distDir, filePath);
    mkdirSync(dirname(fullPath), { recursive: true });

    if (entry.encoding === "base64") {
      writeFileSync(fullPath, Buffer.from(entry.body, "base64"));
    } else {
      writeFileSync(fullPath, entry.body, "utf8");
    }
    count++;
  }

  console.log(`Extracted ${count} files to ${distDir}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
