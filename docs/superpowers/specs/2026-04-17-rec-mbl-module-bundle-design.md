# REC + MBL Module Bundle Migration Design

## Goal

Replace the current `SMDB` / `sds.bundle` module-bundle container with a canonical SDS-owned `MBL` record carried inside the existing appended `REC` trailer, then publish aligned releases of `spacedatastandards.org`, `space-data-module-sdk`, and `@spacedatanetwork/sdn-js`.

## Problem Summary

The current single-file module artifact shape violates SDS conventions in two ways:

1. The module bundle descriptor uses `file_identifier "SMDB"` instead of the SDS `$XXX` convention.
2. The module bundle descriptor is not part of `REC`, so a single plugin artifact currently uses two independent container formats:
   - a WASM custom section named `sds.bundle`
   - an appended `REC` trailer containing `ENC` / `PNM`

This splits metadata across two containers, keeps module bundle semantics outside SDS ownership, and creates downstream versioning and compatibility drift.

## Current State

### Version Drift

- `spacedatastandards.org` local package version is `1.79.0+1773945434393`.
- `space-data-module-sdk` package version is `0.7.0` and still depends on `spacedatastandards.org` via a git SHA.
- `@spacedatanetwork/sdn-js` package version is `2.0.2`.
- `sdn-js` declares published `spacedatastandards.org@1.84.1`, but its lockfile currently resolves multiple distinct copies of `spacedatastandards.org`.

That dependency graph is not acceptable for a schema-level format migration. The release train must converge on one published SDS version at each stage.

### Code Ownership

- `spacedatastandards.org` owns canonical schema definitions and generated bindings.
- `space-data-module-sdk` owns single-file bundle creation/parsing and publication-protection packaging.
- `@spacedatanetwork/sdn-js` only consumes built artifacts and currently has a browser inspector that still reads the legacy bundle custom section through SDK helpers.

## Decision

Use a hard cutover with no backward compatibility for `SMDB` / `sds.bundle`.

### Rejected Alternatives

#### Transitional compatibility bridge

Writing `MBL` while preserving readers for `SMDB` / `sds.bundle` would reduce rollout risk, but it violates the stated non-goal of preserving the broken format and prolongs the incompatible container contract.

#### SDK-local or consumer-local shim

Changing the SDK or `sdn-js` without first introducing a canonical SDS schema would create another shadow format and violate the ownership boundary that `spacedatastandards.org` defines canonical records.

## Target Architecture

### New SDS Record

Add `schema/MBL/main.fbs` to `spacedatastandards.org`.

The new record code is `MBL`, with:

- `root_type MBL`
- `file_identifier "$MBL"`

`MBL` replaces the current vendor-local `ModuleBundle` structure and carries:

- bundle format/version metadata needed for artifact interpretation
- canonicalization rule
- canonical module hash
- manifest hash
- legacy manifest export symbol
- legacy manifest size symbol
- module bundle entry list
- bundle entry role enum
- bundle entry payload encoding enum
- any other fields required to preserve current `ModuleBundle` semantics without a parallel local schema

The intent is semantic parity with the current `ModuleBundle`, but with SDS ownership, SDS naming, and SDS-generated bindings.

### REC Integration

`schema/REC/main.fbs` must include `MBL` and allow it inside `RecordType`.

Required changes:

- add `include "../MBL/main.fbs";`
- add `MBL` to `union RecordType`

`REC` remains the canonical heterogeneous artifact container.

### Artifact Layout

The module artifact shape becomes:

1. raw WASM bytes
2. one appended `REC` trailer

The appended trailer layout does not change:

- trailing REC FlatBuffer bytes
- existing 8-byte footer layout: `[uint32_le_length]["$REC"]`

The `REC.RECORDS` array must contain:

- `MBL` always
- `ENC` when encrypted delivery metadata is present
- `PNM` when provider/network metadata is present
- any additional SDS records already justified by the current artifact path

The WASM must not contain a custom section named `sds.bundle`.

## Repository Changes

### 1. `spacedatastandards.org`

#### Schema changes

- add `schema/MBL/main.fbs`
- regenerate `schema/REC/main.fbs` so `MBL` appears in the include list and `RecordType` union

#### Generation changes

Run the normal standards build pipeline so all generated outputs include `MBL`:

- schema version bumping
- JS/TS bindings
- Go bindings
- Python bindings
- any bundled schema archives and indexes

#### Release requirement

Publish the updated standards package first. Downstream repos must consume the published version, not a git SHA and not repo-local generated code.

### 2. `space-data-module-sdk`

#### Container removal

Delete the legacy format and its supporting code:

- `schemas/ModuleBundle.fbs`
- generated `orbpro.module.ModuleBundle*` outputs derived from that schema
- `src/bundle/` implementation that encodes/decodes `SMDB`
- `sds.bundle` custom-section writer/reader logic

#### Replacement behavior

Rework:

- `createSingleFileBundle()`
- `parseSingleFileBundle()`
- `protectModuleArtifact()`

So they operate entirely through REC+MBL.

`createSingleFileBundle()` must:

- preserve the canonical WASM module bytes
- construct an `MBL` SDS record using generated `spacedatastandards.org` bindings
- append a `REC` trailer containing `MBL` and existing `ENC` / `PNM` records where applicable

`parseSingleFileBundle()` must:

- read only the appended `REC` trailer
- locate the `MBL` record in `REC.RECORDS`
- reconstruct the same high-level bundle information the current parser returns

`protectModuleArtifact()` must:

- follow the same REC+MBL write path
- stop producing any custom bundle section

#### Test and doc updates

Update or replace:

- module bundle tests
- CLI tests
- vector-generation tests
- Go/Python reference artifact tests
- README and docs that describe `sds.bundle` or `SMDB`
- compliance checks and example generators

All bundle-focused assertions must move from custom-section presence to REC+MBL presence.

### 3. `@spacedatanetwork/sdn-js`

The live request/grant/fetch/decrypt flow should remain mostly unchanged because it moves encrypted bytes and REC-backed metadata rather than owning the old bundle custom section.

The main consumer change is the browser/runtime bundle inspection path:

- replace the current custom-section reader in `ui/src/browser-bundle.ts`
- stop using SDK helpers that depend on `ModuleBundle` / `sds.bundle`
- read the appended REC trailer and extract the `MBL` record instead

Test fixtures that currently synthesize `ModuleBundle` bytes must be rewritten to synthesize REC+MBL artifacts.

`sdn-js` should also be checked for:

- `sds.bundle`
- `SMDB`
- `ModuleBundle`
- `parseSingleFileBundle`
- `createSingleFileBundle`

Any surviving usage in the runtime path must be migrated or removed.

## Release Train

This change ships as one coordinated sequence:

1. Publish `spacedatastandards.org` with `MBL` and REC union updates.
2. Update `space-data-module-sdk` to the published SDS version, remove `SMDB` / `sds.bundle`, rebuild, and publish.
3. Update `@spacedatanetwork/sdn-js` to the published SDK and SDS versions, migrate the browser bundle reader, rebuild, and publish.
4. Update and rerun the external live E2E harness at `~/test/space-data-network-external-e2e/external-live-e2e.mjs`.

At every stage, dependencies must resolve to published packages, not git SHAs.

## Verification Requirements

### `spacedatastandards.org`

- `MBL` exists in the schema tree
- `REC/main.fbs` includes `MBL`
- `RecordType` contains `MBL`
- generated bindings expose `MBL` in all required languages
- the published npm package contains the generated outputs for `MBL`

### `space-data-module-sdk`

- `grep -rn 'file_identifier "SMDB"' ...` returns no matches in the SDK-owned schema tree
- `grep -rn '"sds.bundle"' ...` returns no matches in SDK source implementing artifact packaging
- freshly built plugin artifacts contain an appended REC trailer whose record list includes `MBL`
- no custom WASM section named `sds.bundle` exists
- `ENC` / `PNM` records still round-trip where applicable

### `@spacedatanetwork/sdn-js`

- browser/runtime artifact parsing succeeds against REC+MBL output
- no remaining runtime dependency on the legacy custom-section path
- the existing live delivery path still fetches, decrypts, loads, and invokes correctly

### Integrated E2E

The external harness at `~/test/space-data-network-external-e2e/external-live-e2e.mjs` must be updated as needed and rerun against a freshly built artifact using the REC+MBL format.

## Definition Of Done

- `grep -rn 'file_identifier "SMDB"' packages/space-data-module-sdk/schemas/` returns no matches
- `grep -rn '"sds.bundle"' packages/space-data-module-sdk/src/` returns no matches
- `grep -n 'MBL' packages/spacedatastandards.org/schema/REC/main.fbs` shows `MBL` in both the include list and `RecordType`
- a newly built plugin artifact parses into a REC record array containing at minimum `MBL`
- no custom WASM section named `sds.bundle` remains in output artifacts
- downstream packages consume published SDS and SDK versions only

## Non-Goals

- preserve backward compatibility with `SMDB` or `sds.bundle`
- change the appended REC trailer magic or footer layout
- change OrbPro consumer code in the same workstream beyond what is required to validate the upstream release train

## Confirmed Constraints

- `MBL` is the accepted new SDS record code for this migration.
- All required `ModuleBundle` semantics must be represented directly in SDS without keeping any repo-local shadow schema.
- The existing external E2E harness should be updated in the same release train after upstream packages publish.
