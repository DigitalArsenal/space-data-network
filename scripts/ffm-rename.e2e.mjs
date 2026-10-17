#!/usr/bin/env node

import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { DEFAULT_ORBPRO_MODULES } from "./seed-orbpro-module-catalog.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const legacyName = ["hp", "op"].join("");
const legacyModuleId = `com.orbpro.${legacyName}`;
const artifactPath =
  "packages/orbpro-integration/propagator.ffm/dist/ffm-encrypted.js";
const staleSds = process.argv.slice(2).includes("--stale-sds");
const failures = [];

async function check(label, action) {
  try {
    await action();
    console.log(`PASS ${label}`);
  } catch (error) {
    failures.push(label);
    console.error(`FAIL ${label}: ${error.message}`);
  }
}

async function scan(directory) {
  const entries = await fs.readdir(path.join(root, directory), {
    withFileTypes: true,
  });
  const result = { files: 0, occurrences: 0, matches: [] };
  for (const entry of entries.sort((left, right) =>
    left.name.localeCompare(right.name, "en"),
  )) {
    const relativePath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      const nested = await scan(relativePath);
      result.files += nested.files;
      result.occurrences += nested.occurrences;
      result.matches.push(...nested.matches);
    } else if (entry.isFile()) {
      result.files += 1;
      const source = await fs.readFile(path.join(root, relativePath), "utf8");
      const occurrences = source.toLowerCase().split(legacyName).length - 1;
      result.occurrences += occurrences;
      if (occurrences > 0) {
        result.matches.push(`${relativePath} (${occurrences})`);
      }
    } else {
      throw new Error(`Cannot scan nonregular entry: ${relativePath}`);
    }
  }
  return result;
}

await check("arguments", () => {
  assert.ok(
    process.argv.slice(2).every((argument) => argument === "--stale-sds"),
    "Only --stale-sds is supported, when the SDS refresh was skipped",
  );
});

await check("grant policy uses com.orbpro.ffm with no legacy rule", async () => {
  const policy = JSON.parse(
    await fs.readFile(path.join(root, "deployment/license/grant-policy.json"), "utf8"),
  );
  assert.ok(Array.isArray(policy.modules), "Policy must contain module rules");
  assert.equal(policy.modules.filter((rule) => rule.match === "com.orbpro.ffm").length, 1);
  assert.ok(policy.modules.every((rule) => rule.match !== legacyModuleId));
});

await check("FFM catalog slug, artifact path, and encryptedData export", () => {
  const entries = DEFAULT_ORBPRO_MODULES.filter((entry) => entry.slug === "ffm");
  assert.equal(entries.length, 1, "Catalog must contain exactly one FFM entry");
  assert.equal(entries[0].protectedModulePath, artifactPath);
  assert.deepEqual(entries[0].protectedExports, [
    { exportName: "encryptedData", slug: "ffm" },
  ]);
  assert.ok(!JSON.stringify(DEFAULT_ORBPRO_MODULES).toLowerCase().includes(legacyName));
});

for (const directory of [
  "scripts",
  "deployment",
  "sdn-server/internal/license",
  "sdn-server/internal/node",
]) {
  await check(`legacy-name scan: ${directory}`, async () => {
    const result = await scan(directory);
    const reportOnly = false;
    console.log(
      `  ${result.files} files; ${result.occurrences} legacy-name occurrences` +
      (reportOnly ? "; stale SDS copy (report only)" : ""),
    );
    if (!reportOnly) {
      assert.equal(result.occurrences, 0, result.matches.join("\n"));
    }
  });
}

if (failures.length > 0) {
  console.error(`FFM rename E2E FAILED: ${failures.length} check(s)`);
  process.exitCode = 1;
} else {
  console.log(`FFM rename E2E PASSED${staleSds ? " (stale SDS copies reported only)" : ""}`);
}
