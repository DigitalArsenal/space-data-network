# REC + MBL Module Bundle Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the legacy `SMDB` / `sds.bundle` module bundle format with an SDS-owned `MBL` record embedded in the appended `REC` trailer, align dependency versions to published releases, and verify the new artifact end-to-end.

**Architecture:** `spacedatastandards.org` defines the new `MBL` schema and updates `REC`; `space-data-module-sdk` stops emitting a WASM custom section and instead writes/reads `MBL` through the existing REC trailer; `@spacedatanetwork/sdn-js` updates its browser/runtime bundle inspection path to consume REC+MBL only. Releases happen in order: SDS, SDK, then `sdn-js`.

**Tech Stack:** FlatBuffers, `spacedatastandards.org`, `space-data-module-sdk`, `@spacedatanetwork/sdn-js`, npm, Vitest, existing REC trailer helpers, external E2E harness.

---

### Task 1: Align Dependency Baselines And Guardrails

**Files:**
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/package.json`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/package-lock.json`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/package.json`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/package-lock.json`
- Test: version-resolution commands in all three repos

- [ ] **Step 1: Capture the failing version state**

Run:

```bash
node -p "require('/Users/tj/software/OrbPro/packages/space-data-module-sdk/package.json').dependencies['spacedatastandards.org']"
node -p "require('/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/package.json').dependencies['spacedatastandards.org']"
```

Expected: SDK points to a git SHA while `sdn-js` points at a published version.

- [ ] **Step 2: Add red tests or checks that reject git-SHA SDS dependencies in runtime packages**

Use lightweight assertions in existing package/tests or a new version-check test so this fails before the fix:

```js
assert.match(pkg.dependencies['spacedatastandards.org'], /^\d/);
assert.doesNotMatch(pkg.dependencies['spacedatastandards.org'], /^git\+/);
```

- [ ] **Step 3: Update package manifests to use published versions only**

Change the SDK and `sdn-js` dependency declarations so they both consume published SDS versions, and later update them again after the new SDS publish.

- [ ] **Step 4: Refresh lockfiles**

Run:

```bash
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm install
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npm install
```

Expected: no git-based `spacedatastandards.org` entry remains in the direct dependency graph.

- [ ] **Step 5: Commit**

```bash
git -C /Users/tj/software/OrbPro/packages/space-data-module-sdk add package.json package-lock.json
git -C /Users/tj/software/OrbPro/packages/space-data-module-sdk commit -m "chore: align sdk standards dependency policy"
```

### Task 2: Add `MBL` To SDS And Regenerate Bindings

**Files:**
- Create: `/Users/tj/software/OrbPro/packages/spacedatastandards.org/schema/MBL/main.fbs`
- Modify: `/Users/tj/software/OrbPro/packages/spacedatastandards.org/schema/REC/main.fbs`
- Modify: `/Users/tj/software/OrbPro/packages/spacedatastandards.org/package.json` (version bump via build scripts if applicable)
- Modify: generated outputs under `/Users/tj/software/OrbPro/packages/spacedatastandards.org/lib/`
- Test: schema generation/build outputs

- [ ] **Step 1: Write a red schema assertion for `MBL` in REC**

Add or extend a schema-generation test/check so it fails if `REC` does not include `MBL`:

```python
assert 'include "../MBL/main.fbs";' in rec_source
assert re.search(r"\bMBL\b", record_type_union)
```

- [ ] **Step 2: Add the `MBL` schema**

Create `schema/MBL/main.fbs` with SDS naming:

```fbs
namespace SDS.MBL;

table MBL {
  ...
}

root_type MBL;
file_identifier "$MBL";
```

Carry forward the current `ModuleBundle` semantics:
- canonicalization rule
- canonical module hash
- manifest hash
- legacy manifest export/size symbols
- entry list with role and payload encoding

- [ ] **Step 3: Add `MBL` to REC**

Update or regenerate `schema/REC/main.fbs` so it contains:

```fbs
include "../MBL/main.fbs";

union RecordType {
  ...
  MBL,
  ...
}
```

- [ ] **Step 4: Regenerate bindings and artifacts**

Run:

```bash
cd /Users/tj/software/OrbPro/packages/spacedatastandards.org && npm run build
```

Expected: JS/TS/Go/Python outputs for `MBL` exist and REC references them correctly.

- [ ] **Step 5: Verify generated outputs**

Run:

```bash
rg -n '\bMBL\b' /Users/tj/software/OrbPro/packages/spacedatastandards.org/schema/REC/main.fbs /Users/tj/software/OrbPro/packages/spacedatastandards.org/lib
```

Expected: `MBL` appears in both REC and generated bindings.

- [ ] **Step 6: Commit**

```bash
git -C /Users/tj/software/OrbPro/packages/spacedatastandards.org add schema lib package.json package-lock.json
git -C /Users/tj/software/OrbPro/packages/spacedatastandards.org commit -m "feat: add MBL record to SDS REC"
```

### Task 3: Replace SDK `SMDB` / `sds.bundle` With REC + MBL

**Files:**
- Delete: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/schemas/ModuleBundle.fbs`
- Delete or replace: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/src/bundle/`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/src/compiler/compileModule.js`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/src/deployment/index.js`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/src/index.d.ts`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/src/standards/index.js`
- Test: bundle and artifact tests under `/Users/tj/software/OrbPro/packages/space-data-module-sdk/test/`

- [ ] **Step 1: Write failing SDK tests for the new container shape**

Add or rewrite tests so they expect:

```js
assert.equal(getWasmCustomSections(bundleBytes, 'sds.bundle').length, 0);
assert.ok(parsed.rec?.records?.some((record) => record.rootType === 'MBL'));
```

And fail under the current implementation.

- [ ] **Step 2: Build an `MBL` codec on top of published SDS bindings**

Replace `ModuleBundle`-specific encode/decode helpers with helpers that:
- create an `MBL` record
- append it into REC
- recover bundle metadata from REC

The core write path should look conceptually like:

```js
const recBytes = encodeREC({
  records: [
    createMBLRecord(bundleDescriptor),
    ...existingRecords
  ]
});
return appendRECTrailer(wasmBytes, recBytes);
```

- [ ] **Step 3: Rework `createSingleFileBundle()`**

Remove custom section emission and make it produce:
- plain WASM bytes
- appended REC trailer with `MBL`

- [ ] **Step 4: Rework `parseSingleFileBundle()`**

Read the REC trailer only and extract `MBL`:

```js
const rec = parseRECTrailer(bytes);
const mbl = rec.records.find((record) => record.rootType === 'MBL');
```

Return the same high-level data shape required by SDK consumers.

- [ ] **Step 5: Rework `protectModuleArtifact()`**

Ensure the protected artifact path reuses the REC+MBL bundle implementation with no legacy side path.

- [ ] **Step 6: Remove legacy schema/generated outputs**

Delete `ModuleBundle.fbs` and stop generating or shipping its bindings and example outputs.

- [ ] **Step 7: Run focused SDK tests**

Run:

```bash
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm test -- module-bundle.test.js
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm test -- module-bundle-vectors.test.js
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm test -- module-bundle-cli.test.js
```

Expected: no `sds.bundle` sections, REC contains `MBL`.

- [ ] **Step 8: Commit**

```bash
git -C /Users/tj/software/OrbPro/packages/space-data-module-sdk add schemas src test
git -C /Users/tj/software/OrbPro/packages/space-data-module-sdk commit -m "feat: move module bundle container into REC MBL"
```

### Task 4: Update SDK Docs, Examples, And Cross-Language Fixtures

**Files:**
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/README.md`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/docs/`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/examples/single-file-bundle/`
- Modify: `/Users/tj/software/OrbPro/packages/space-data-module-sdk/test/`

- [ ] **Step 1: Write failing doc/example assertions where the old format is referenced**

Search for stale references:

```bash
rg -n 'sds.bundle|SMDB|ModuleBundle' /Users/tj/software/OrbPro/packages/space-data-module-sdk
```

Expected initially: many matches.

- [ ] **Step 2: Rewrite docs to describe REC + MBL**

Replace text like:

```md
WASM custom section named `sds.bundle`
```

With:

```md
Appended REC trailer containing an `MBL` record and related SDS records.
```

- [ ] **Step 3: Update example generators and language fixtures**

Ensure Go/Python/CLI vectors parse REC+MBL and no longer check `SMDB` or `sds.bundle`.

- [ ] **Step 4: Re-run compliance and vector generation**

Run:

```bash
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm run check:compliance
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm run generate:vectors
```

- [ ] **Step 5: Commit**

```bash
git -C /Users/tj/software/OrbPro/packages/space-data-module-sdk add README.md docs examples test
git -C /Users/tj/software/OrbPro/packages/space-data-module-sdk commit -m "docs: update sdk bundle docs for REC MBL"
```

### Task 5: Migrate `sdn-js` Browser Bundle Parsing To REC + MBL

**Files:**
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/ui/src/browser-bundle.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/src/ui/runtime/browser-bundle.test.ts`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/package.json`
- Modify: `/Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js/package-lock.json`

- [ ] **Step 1: Write a failing `sdn-js` test for REC+MBL parsing**

Replace the old custom-section fixture with a REC+MBL artifact assertion:

```ts
expect(readBundleMetadata(bundleBytes).records.some((record) => record.rootType === 'MBL')).toBe(true);
expect(findCustomSection(bundleBytes, 'sds.bundle')).toBeUndefined();
```

- [ ] **Step 2: Replace the browser custom-section reader**

Rework `ui/src/browser-bundle.ts` so it:
- reads the appended REC trailer
- finds the `MBL` record
- extracts manifest-entry metadata from `MBL`, not `ModuleBundle`

- [ ] **Step 3: Remove old runtime imports**

Delete imports that rely on `decodeModuleBundle`, `decodeModuleBundleEntryPayload`, `findModuleBundleEntry`, or any SDK helper that still implies `sds.bundle`.

- [ ] **Step 4: Refresh package dependencies to published SDS and SDK releases**

Update `package.json` and `package-lock.json` after the upstream releases are available.

- [ ] **Step 5: Run focused `sdn-js` tests**

Run:

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/runtime/browser-bundle.test.ts
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npx vitest run src/ui/runtime/live-delivery.test.ts src/module-delivery-observer.test.ts
```

- [ ] **Step 6: Commit**

```bash
git -C /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui add sdn-js
git -C /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui commit -m "feat: parse REC MBL bundles in sdn-js"
```

### Task 6: Full Verification, Rebuild, External E2E, And Release

**Files:**
- Modify: release/version metadata in all three repos as required
- Modify: `~/test/space-data-network-external-e2e/external-live-e2e.mjs`
- Test: repo builds, package tests, external E2E, publish and CI monitors

- [ ] **Step 1: Run SDS verification**

```bash
cd /Users/tj/software/OrbPro/packages/spacedatastandards.org && npm run build
cd /Users/tj/software/OrbPro/packages/spacedatastandards.org && npm pack --dry-run
```

- [ ] **Step 2: Run SDK verification**

```bash
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm test
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm run check:compliance
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm pack --dry-run
```

- [ ] **Step 3: Run `sdn-js` verification**

```bash
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npm run build
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npm test
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npm pack --dry-run
```

- [ ] **Step 4: Run definition-of-done greps**

```bash
grep -rn 'file_identifier "SMDB"' /Users/tj/software/OrbPro/packages/space-data-module-sdk/schemas/
grep -rn '"sds.bundle"' /Users/tj/software/OrbPro/packages/space-data-module-sdk/src/
grep -n 'MBL' /Users/tj/software/OrbPro/packages/spacedatastandards.org/schema/REC/main.fbs
```

Expected:
- first grep: no matches
- second grep: no matches
- third grep: `MBL` present in include list and union

- [ ] **Step 5: Rebuild a real plugin artifact and inspect it**

Use the SDK to build a fresh module artifact, then assert:

```js
assert.ok(parsedRec.records.some((record) => record.rootType === 'MBL'));
assert.equal(getWasmCustomSections(wasmBytes, 'sds.bundle').length, 0);
```

- [ ] **Step 6: Update and run the external E2E harness**

```bash
cd /Users/tj/test/space-data-network-external-e2e && node external-live-e2e.mjs
```

Expected: requester node, real grant, encrypted fetch, unwrap, decrypt, SDK load, and invoke still succeed with the REC+MBL artifact shape.

- [ ] **Step 7: Publish in order and monitor CI**

```bash
cd /Users/tj/software/OrbPro/packages/spacedatastandards.org && npm publish
cd /Users/tj/software/OrbPro/packages/space-data-module-sdk && npm publish
cd /Users/tj/software/space-data-network/.worktrees/codex-sdn-browser-ui/sdn-js && npm publish
```

Then monitor the relevant GitHub Actions workflows and npm availability before moving downstream.

- [ ] **Step 8: Final commit(s)**

Use intentional per-repo commits for the final release-ready state if any verification-only edits remain.
