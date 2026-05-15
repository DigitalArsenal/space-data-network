/**
 * Version consistency script tests.
 *
 * Run: node tests/isomorphic/version-consistency.test.mjs
 */

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const PASS = "\x1b[32mPASS\x1b[0m";
const FAIL = "\x1b[31mFAIL\x1b[0m";
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..", "..");

async function test(name, fn) {
  try {
    await fn();
    console.log(`${PASS}: ${name}`);
  } catch (error) {
    console.error(`${FAIL}: ${name}`);
    console.error(error);
    process.exitCode = 1;
  }
}

await test("check-version-consistency accepts production Git dependency tags without throwing", () => {
  const result = spawnSync(
    process.execPath,
    ["scripts/check-version-consistency.js"],
    {
      cwd: repoRoot,
      encoding: "utf8",
    },
  );
  const output = `${result.stdout}\n${result.stderr}`;

  assert.equal(result.signal, null);
  assert.match(result.stdout, /Checks run:/);
  assert.match(
    output,
    /sdn-js flatsql uses production Git tag v1\.0\.1/,
  );
  assert.match(
    output,
    /sdn-js spacedatastandards\.org uses production Git tag v1\.100\.0/,
  );
  assert.doesNotMatch(output, /TypeError|Cannot read properties/);
});

await test("check-version-consistency reports missing external Git release artifacts", () => {
  const result = spawnSync(
    process.execPath,
    ["scripts/check-version-consistency.js"],
    {
      cwd: repoRoot,
      encoding: "utf8",
      env: {
        ...process.env,
        SDN_VERSION_CHECK_MOCK_NPM_VERSIONS: JSON.stringify({
          flatsql: "1.0.0",
          "spacedatastandards.org": "1.99.0",
        }),
        SDN_VERSION_CHECK_MOCK_GIT_TAGS: JSON.stringify({
          flatsql: ["v1.0.0"],
          "spacedatastandards.org": ["v1.99.0"],
        }),
        SDN_VERSION_CHECK_MOCK_GO_VERSIONS: JSON.stringify({
          "github.com/DigitalArsenal/spacedatastandards.org/lib/go": [
            "v1.98.0",
            "v1.99.0",
          ],
        }),
        SDN_VERSION_CHECK_MOCK_GO_DIRECT_VERSIONS: JSON.stringify({
          "github.com/DigitalArsenal/spacedatastandards.org/lib/go": [
            "v1.98.0",
            "v1.99.0",
          ],
        }),
      },
    },
  );
  const output = `${result.stdout}\n${result.stderr}`;

  assert.equal(result.signal, null);
  assert.match(
    output,
    /Git dependency flatsql does not expose tag v1\.0\.1/,
  );
  assert.match(
    output,
    /Git dependency spacedatastandards\.org does not expose tag v1\.100\.0/,
  );
  assert.match(
    output,
    /Go module github\.com\/DigitalArsenal\/spacedatastandards\.org\/lib\/go does not expose v1\.100\.0/,
  );
});

await test("check-version-consistency accepts direct Go module tag visibility while proxy cache lags", () => {
  const result = spawnSync(
    process.execPath,
    ["scripts/check-version-consistency.js"],
    {
      cwd: repoRoot,
      encoding: "utf8",
      env: {
        ...process.env,
        SDN_VERSION_CHECK_MOCK_NPM_VERSIONS: JSON.stringify({
          flatsql: "1.0.1",
          "spacedatastandards.org": "1.100.0",
        }),
        SDN_VERSION_CHECK_MOCK_GIT_TAGS: JSON.stringify({
          flatsql: ["v1.0.1"],
          "spacedatastandards.org": ["v1.100.0"],
        }),
        SDN_VERSION_CHECK_MOCK_GO_VERSIONS: JSON.stringify({
          "github.com/DigitalArsenal/spacedatastandards.org/lib/go": [
            "v1.98.0",
            "v1.99.0",
          ],
        }),
        SDN_VERSION_CHECK_MOCK_GO_DIRECT_VERSIONS: JSON.stringify({
          "github.com/DigitalArsenal/spacedatastandards.org/lib/go": [
            "v1.98.0",
            "v1.99.0",
            "v1.100.0",
          ],
        }),
      },
    },
  );
  const output = `${result.stdout}\n${result.stderr}`;

  assert.equal(result.signal, null);
  assert.match(
    output,
    /Go module github\.com\/DigitalArsenal\/spacedatastandards\.org\/lib\/go exposes v1\.100\.0 via direct Git lookup/,
  );
  assert.doesNotMatch(
    output,
    /Go module github\.com\/DigitalArsenal\/spacedatastandards\.org\/lib\/go does not expose v1\.100\.0/,
  );
});
