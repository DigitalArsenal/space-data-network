import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..", "..");
const seedScriptPath = path.join(repoRoot, "scripts", "seed-orbpro-module-catalog.mjs");

const seedScript = await fs.readFile(seedScriptPath, "utf8");

assert.match(
  seedScript,
  /slug:\s*"wasm-engine"[\s\S]*?protectedModulePath:\s*"packages\/wasm-engine\/dist\/wasm-engine-sdn-encrypted\.js"/,
  "DEFAULT_ORBPRO_MODULES must seed the SDN-encrypted wasm-engine protected artifact",
);

assert.doesNotMatch(
  seedScript,
  /protectedModulePath:\s*"packages\/wasm-engine\/dist\/wasm-engine-encrypted\.js"/,
  "DEFAULT_ORBPRO_MODULES must not seed the legacy wasm-engine-encrypted.js artifact",
);

assert.match(
  seedScript,
  /slug:\s*"wasm-engine"[\s\S]*?moduleId:\s*"com\.orbpro\.wasm-engine"/,
  "the wasm-engine seed entry must declare the canonical com.orbpro.wasm-engine moduleId",
);

assert.match(
  seedScript,
  /slug:\s*"wasm-engine"[\s\S]*?exportName:\s*"encryptedData"[\s\S]*?slug:\s*"wasm-engine"/,
  "the wasm-engine seed entry must publish its encryptedData export as wasm-engine",
);

assert.match(
  seedScript,
  /slug:\s*"wasm-engine"[\s\S]*?keyExport:\s*"recipientPrivateKeyHex"/,
  "the wasm-engine seed entry must write the recipient key used by module delivery",
);
