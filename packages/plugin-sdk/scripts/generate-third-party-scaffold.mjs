#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");

function parseArgs(argv) {
  const out = {
    type: "",
    name: "",
    outDir: process.cwd(),
    vendorId: "example-vendor",
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];

    if (arg === "--type" && next) {
      out.type = String(next).trim().toLowerCase();
      i++;
      continue;
    }
    if (arg === "--name" && next) {
      out.name = String(next).trim();
      i++;
      continue;
    }
    if (arg === "--out-dir" && next) {
      out.outDir = path.resolve(next);
      i++;
      continue;
    }
    if (arg === "--vendor-id" && next) {
      out.vendorId = String(next).trim();
      i++;
      continue;
    }
    if (arg === "--help" || arg === "-h") {
      printUsage();
      process.exit(0);
    }
  }

  if (out.type !== "client" && out.type !== "server") {
    throw new Error("--type must be client or server");
  }
  if (!out.name) {
    throw new Error("--name is required");
  }

  return out;
}

function printUsage() {
  console.log(`Usage:\n  node scripts/generate-third-party-scaffold.mjs --type client|server --name <plugin-name> [options]\n\nOptions:\n  --out-dir <path>     Destination parent directory (default: cwd)\n  --vendor-id <id>     Vendor identifier (default: example-vendor)\n  --help               Show this help\n`);
}

function toSlug(value) {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+/, "")
    .replace(/-+$/, "")
    .slice(0, 64);
}

async function copyTemplateRecursive(sourceDir, targetDir, replacements) {
  await fs.mkdir(targetDir, { recursive: true });
  const entries = await fs.readdir(sourceDir, { withFileTypes: true });

  for (const entry of entries) {
    const sourcePath = path.join(sourceDir, entry.name);
    const targetPath = path.join(targetDir, entry.name);

    if (entry.isDirectory()) {
      await copyTemplateRecursive(sourcePath, targetPath, replacements);
      continue;
    }

    let content = await fs.readFile(sourcePath, "utf8");
    for (const [token, value] of Object.entries(replacements)) {
      content = content.split(token).join(value);
    }
    await fs.writeFile(targetPath, content, "utf8");
  }
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const slug = toSlug(args.name);
  const pluginId = `com.${args.vendorId.replace(/[^a-zA-Z0-9]/g, "")}.${slug}`;

  const templateDir = path.join(
    packageRoot,
    "templates",
    args.type === "client"
      ? "third-party-client-plugin"
      : "third-party-server-plugin",
  );
  const outputDir = path.join(args.outDir, slug);

  await fs.access(templateDir);
  await copyTemplateRecursive(templateDir, outputDir, {
    "__PLUGIN_NAME__": args.name,
    "__PLUGIN_ID__": pluginId,
    "__VENDOR_ID__": args.vendorId,
  });

  console.log(
    `Generated ${args.type} plugin scaffold: ${outputDir}`,
  );
  console.log(`plugin_id: ${pluginId}`);
}

main().catch((error) => {
  console.error(error.message || error);
  process.exit(1);
});
