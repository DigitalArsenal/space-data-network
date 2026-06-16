# SDN Beta Artifacts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a beta CI/CD lane that publishes usable Space Data Network artifacts with beta release numbers and exposes those downloads on the SDN website.

**Architecture:** Keep the signed production release workflow unchanged. Add a separate beta workflow that computes a beta release tag, reuses the existing IPFS, package, Docker, and JavaScript build paths, assembles a prerelease artifact directory, and publishes it as a GitHub prerelease. The static website and README link users to the beta channel and name the expected artifacts.

**Tech Stack:** GitHub Actions, Node.js helper scripts, Bash artifact assembly, existing Go/npm/IPFS/nFPM/Syft release tooling, static HTML docs.

---

### Task 1: Beta Release Version Helper

**Files:**
- Create: `deployment/release/prepare-beta-release.mjs`
- Create: `deployment/release/prepare-beta-release.test.mjs`

- [x] **Step 1: Write the failing test**

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { computeBetaRelease, formatGithubOutput } from './prepare-beta-release.mjs';

test('defaults to package version plus beta run number', () => {
  assert.deepEqual(computeBetaRelease({ packageVersion: '1.0.3', runNumber: '42' }), {
    releaseTag: 'v1.0.3-beta.42',
    packageVersion: '1.0.3-beta.42',
    releaseName: 'Space Data Network v1.0.3-beta.42 Beta',
    channel: 'beta',
    npmTag: 'beta'
  });
});

test('normalizes explicit beta version to a v-prefixed release tag', () => {
  assert.equal(computeBetaRelease({ packageVersion: '1.0.3', runNumber: '42', inputVersion: '1.2.0-beta.7' }).releaseTag, 'v1.2.0-beta.7');
});

test('rejects non-beta versions', () => {
  assert.throws(() => computeBetaRelease({ packageVersion: '1.0.3', runNumber: '42', inputVersion: 'v1.2.0' }), /must be a beta version/);
});

test('formats GitHub output lines', () => {
  assert.equal(formatGithubOutput({ releaseTag: 'v1.0.3-beta.42', packageVersion: '1.0.3-beta.42', releaseName: 'Space Data Network v1.0.3-beta.42 Beta', channel: 'beta', npmTag: 'beta' }), [
    'release_tag=v1.0.3-beta.42',
    'package_version=1.0.3-beta.42',
    'release_name=Space Data Network v1.0.3-beta.42 Beta',
    'channel=beta',
    'npm_tag=beta'
  ].join('\n'));
});
```

- [x] **Step 2: Run test to verify it fails**

Run: `node --test deployment/release/prepare-beta-release.test.mjs`
Expected: FAIL because `prepare-beta-release.mjs` does not exist yet.

- [x] **Step 3: Write minimal implementation**

Create an ES module that exports `computeBetaRelease()` and `formatGithubOutput()`, reads `package.json` when run directly, writes outputs to `$GITHUB_OUTPUT`, and rejects manual versions that do not contain a SemVer `beta` prerelease.

- [x] **Step 4: Run test to verify it passes**

Run: `node --test deployment/release/prepare-beta-release.test.mjs`
Expected: PASS.

### Task 2: Beta Artifact Assembler

**Files:**
- Create: `deployment/release/assemble-beta-release-artifacts.sh`
- Create: `deployment/release/assemble-beta-release-artifacts.test.mjs`

- [x] **Step 1: Write the failing test**

Create a temp `dist` tree with representative package, VM, SBOM, IPFS, container digest, and JS tarball files, execute the assembler with `VERSION=1.0.3-beta.42 RELEASE_TAG=v1.0.3-beta.42`, and assert `dist/release` contains the copied files, `spacedatanetwork-beta-manifest.json`, `spacedatanetwork-checksums.txt`, and `SDN-BETA-RELEASE.md`.

- [x] **Step 2: Run test to verify it fails**

Run: `node --test deployment/release/assemble-beta-release-artifacts.test.mjs`
Expected: FAIL because the assembler does not exist yet.

- [x] **Step 3: Write minimal implementation**

Create a POSIX-compatible Bash script that copies available beta artifacts from `dist`, builds a manifest using `jq`, creates checksums for every released file except the checksum file itself, and writes a release body that says the channel is beta.

- [x] **Step 4: Run test to verify it passes**

Run: `node --test deployment/release/assemble-beta-release-artifacts.test.mjs`
Expected: PASS.

### Task 3: GitHub Actions Beta Workflow

**Files:**
- Create: `.github/workflows/beta-release-artifacts.yml`
- Create: `deployment/release/beta-release-workflow.test.mjs`
- Modify: `.github/workflows/npm-publish-sdn-js.yml`

- [x] **Step 1: Write the failing test**

Assert the beta workflow exists, calls the version helper, publishes a GitHub prerelease with `make_latest: false`, runs the beta assembler, builds a `sdn-js` package tarball, and uses beta naming. Assert the npm publish workflow maps beta release tags to npm dist-tag `beta`.

- [x] **Step 2: Run test to verify it fails**

Run: `node --test deployment/release/beta-release-workflow.test.mjs`
Expected: FAIL because the beta workflow does not exist yet and npm beta mapping is not present.

- [x] **Step 3: Write minimal implementation**

Add a manual beta workflow with version, preflight, IPFS, Docker, packages, sdn-js package, and release jobs. Update the npm publish workflow so explicit beta release tags publish with npm dist-tag `beta`.

- [x] **Step 4: Run test to verify it passes**

Run: `node --test deployment/release/beta-release-workflow.test.mjs`
Expected: PASS.

### Task 4: Website And README Links

**Files:**
- Create: `deployment/release/beta-download-links.test.mjs`
- Modify: `README.md`
- Modify: `docs/index.html`
- Modify: `docs/docs.html`
- Modify: `docs/release-pipeline.md`

- [x] **Step 1: Write the failing test**

Assert README, homepage downloads, installation docs, and release pipeline docs all mention the beta channel, GitHub prereleases URL, Linux full/edge package names, Linux VM tarball, JS package tarball, SBOM, checksums, and GHCR beta images.

- [x] **Step 2: Run test to verify it fails**

Run: `node --test deployment/release/beta-download-links.test.mjs`
Expected: FAIL because the beta website/download copy has not been added yet.

- [x] **Step 3: Write minimal implementation**

Add a beta downloads panel to `docs/index.html`, a beta download block to `README.md`, beta curl guidance to `docs/docs.html`, and beta release notes to `docs/release-pipeline.md`.

- [x] **Step 4: Run test to verify it passes**

Run: `node --test deployment/release/beta-download-links.test.mjs`
Expected: PASS.

### Task 5: Verification

**Files:**
- Read: `AGENTS.md`
- Read: `docs/install-order.md`

- [x] **Step 1: Run focused tests**

Run: `node --test deployment/release/prepare-beta-release.test.mjs deployment/release/assemble-beta-release-artifacts.test.mjs deployment/release/beta-release-workflow.test.mjs deployment/release/beta-download-links.test.mjs`
Expected: PASS.

- [x] **Step 2: Validate workflow and shell syntax**

Run: `bash -n deployment/release/assemble-beta-release-artifacts.sh`
Expected: PASS.

- [x] **Step 3: Run stack-required status checks**

Run: `git submodule status`
Expected: command completes.

Run: `git submodule foreach 'git status --short --branch'`
Expected: command completes.

- [x] **Step 4: Summarize without claiming unrun checks**

Report exact verification commands that ran and any checks intentionally skipped because the beta workflow needs GitHub-hosted Linux builders and release permissions.
