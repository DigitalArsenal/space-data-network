import test from "node:test";
import assert from "node:assert/strict";

import {
  createModuleRunner,
  createBrowserHost,
  loadModule,
} from "../src/index.js";

test("package exports the expected module-runner entry points", () => {
  assert.equal(typeof createModuleRunner, "function");
  assert.equal(typeof createBrowserHost, "function");
  assert.equal(typeof loadModule, "function");
});

test("createModuleRunner requires wasmSource", async () => {
  await assert.rejects(
    createModuleRunner(),
    /wasmSource is required/,
  );
});
