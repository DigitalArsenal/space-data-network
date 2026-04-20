# SDN Admin Vanilla TypeScript Refactor Design

## Summary

This design refactors the `sdn-js` admin UI without changing the product stack. The UI remains a Vite-built, browser-first application written in plain TypeScript and CSS, and it continues to emit compiled JavaScript and CSS artifacts with no framework runtime dependency. The purpose of the refactor is to make the `/admin` UI maintainable by breaking the current orchestration-heavy implementation into focused modules while preserving the isomorphic SDN UI contract:

- the same UI can run from `sdn-js` in a browser-local Helia mode
- the same UI can also be hosted by `sdn-server`
- the output remains static web assets

The current bottleneck is `sdn-js/ui/src/main.ts`, which mixes bootstrap, shell behavior, workspace coordination, runtime orchestration, DOM wiring, and HTML rendering in one file. This refactor moves that work into explicit boundaries so future product changes do not require editing a 1,500-line entrypoint.

## Goals

- Keep the current UI stack as plain TypeScript, plain CSS, and Vite.
- Preserve compiled output as JavaScript and CSS only.
- Refactor the admin UI incrementally so `/admin` remains runnable after each slice.
- Reduce the size and responsibility count of `sdn-js/ui/src/main.ts`.
- Allow DOM structure, IDs, classes, and file names to be renamed where a cleaner structure is warranted.
- Fold in small UI cleanup as part of the refactor when it reduces coupling or simplifies behavior.

## Non-Goals

- No migration to React, Svelte, Vue, or another UI framework.
- No rewrite of the runtime/network/module-delivery logic that already lives under `sdn-js/src/ui/runtime`.
- No product redesign of the admin shell during this refactor.
- No server/API contract change unless a local naming cleanup requires a matching internal adjustment.

## Current State

The current admin UI is split across:

- `sdn-js/ui/src/app.ts`
  - static shell markup and major workspace structure
- `sdn-js/ui/src/main.ts`
  - bootstrap
  - runtime module loading
  - shell event binding
  - provider refresh
  - marketplace refresh/render
  - live delivery workflow
  - address lookup
  - feature carousel behavior
  - directory rendering
  - frontend workspace/editor orchestration
  - wallet modal wiring
  - low-level DOM helpers
- `sdn-js/ui/src/frontend-editor.ts`
  - Monaco editor wrapper
- `sdn-js/src/ui/runtime/*`
  - reusable runtime-facing modules such as admin adapters, marketplace sources, frontend workspace transport, wallet modal, observed peers, and live delivery helpers

The main structural problem is not that the UI lacks reusable primitives. It is that `main.ts` bypasses them by centralizing too much control in one file.

## Design Principles

### Keep Runtime Logic Outside the View Layer

Network, wallet, module-delivery, and discovery logic should stay in plain TypeScript modules and remain consumable independently of the DOM layer.

### Make View Controllers Small and Explicit

Each major workspace or shell concern should have one controller responsible for:

- binding events in its area
- requesting updates from runtime/state modules
- rendering its own markup through view helpers

### Prefer Incremental Safety Over Clever Abstractions

This refactor should not introduce a homemade framework. The code should remain understandable to someone reading plain TypeScript modules and DOM updates.

### Rename for Clarity When It Removes Friction

The refactor is allowed to rename DOM hooks, files, and helper APIs when existing names are misleading or overly global.

## Target Architecture

### 1. Entry and Bootstrap Layer

New responsibility:

- create the app shell
- initialize shared state
- load runtime modules lazily
- wire top-level controllers together

Target files:

- `sdn-js/ui/src/main.ts`
  - reduced to a thin entrypoint that calls `bootstrapAdminApp()`
- `sdn-js/ui/src/bootstrap.ts`
  - actual boot sequence
- `sdn-js/ui/src/runtime-modules.ts`
  - dynamic imports and shared runtime-module access

`main.ts` should stop owning business behavior directly.

### 2. Shared App State Layer

New responsibility:

- hold mutable UI state in one typed structure
- expose focused mutators for provider, node, identity, delivery timeline, observed peers, store selection, and frontend workspace/editor state

Target files:

- `sdn-js/ui/src/state/app-state.ts`
- `sdn-js/ui/src/state/types.ts`

This state layer is not a framework store. It is a typed coordination object used by controllers.

### 3. DOM Utility Layer

New responsibility:

- query helpers
- escaping and formatting helpers
- event delegation helpers
- workspace activation helpers

Target files:

- `sdn-js/ui/src/dom/query.ts`
- `sdn-js/ui/src/dom/escape.ts`
- `sdn-js/ui/src/dom/workspaces.ts`
- `sdn-js/ui/src/dom/events.ts`

This removes general-purpose DOM helpers from the bottom of `main.ts`.

### 4. Shell Controller Layer

New responsibility:

- top bar behavior
- local/server switching
- connect-server prompt
- shell metadata rendering
- feature carousel behavior
- workspace navigation

Target files:

- `sdn-js/ui/src/controllers/admin-shell-controller.ts`
- `sdn-js/ui/src/controllers/feature-carousel-controller.ts`

These controllers own the chrome around the workspaces, not the workspace content itself.

### 5. Workspace Controllers

Each workspace gets its own controller and view helpers.

#### Network

Responsibilities:

- provider descriptor refresh
- observed peer rendering
- delivery timeline rendering
- address lookup
- live delivery execution

Target files:

- `sdn-js/ui/src/controllers/network-workspace-controller.ts`
- `sdn-js/ui/src/views/provider-view.ts`
- `sdn-js/ui/src/views/observed-peers-view.ts`
- `sdn-js/ui/src/views/timeline-view.ts`

#### Store

Responsibilities:

- marketplace refresh
- store search and selection
- spotlight/feed/detail rendering

Target files:

- `sdn-js/ui/src/controllers/store-workspace-controller.ts`
- `sdn-js/ui/src/views/store-view.ts`

#### Directory

Responsibilities:

- directory panel refresh
- rendering server/local identity and user roster state

Target files:

- `sdn-js/ui/src/controllers/directory-workspace-controller.ts`
- `sdn-js/ui/src/views/directory-view.ts`

#### Frontend

Responsibilities:

- workspace creation/selection
- file tree rendering
- upload, save, move, delete actions
- editor setup

Target files:

- `sdn-js/ui/src/controllers/frontend-workspace-controller.ts`
- `sdn-js/ui/src/views/frontend-view.ts`

#### Wallet

Responsibilities:

- wallet modal open/close integration
- shell account-button integration
- wallet workspace trigger behavior

Target files:

- `sdn-js/ui/src/controllers/wallet-controller.ts`

The wallet remains modal-driven and does not require a separate heavy page controller.

### 6. Static Shell Markup Layer

`sdn-js/ui/src/app.ts` remains the static shell factory, but it should become more presentation-only.

Allowed changes:

- cleaner section naming
- cleaner DOM hook structure
- more local IDs and `data-*` hooks
- better grouping of workspace roots and shell action areas

The shell file should not accumulate controller logic.

## Runtime Boundary Rules

The following modules stay framework-free and remain the reusable runtime foundation:

- `sdn-js/src/ui/runtime/admin-state.ts`
- `sdn-js/src/ui/runtime/admin-adapter.ts`
- `sdn-js/src/ui/runtime/local-adapter.ts`
- `sdn-js/src/ui/runtime/server-adapter.ts`
- `sdn-js/src/ui/runtime/live-delivery.ts`
- `sdn-js/src/ui/runtime/marketplace*.ts`
- `sdn-js/src/ui/runtime/frontend-workspace.ts`
- `sdn-js/src/ui/runtime/wallet-*.ts`
- `sdn-js/src/ui/runtime/observed-peers.ts`
- `sdn-js/src/ui/runtime/address-lookup.ts`

The refactor may add small helper exports to these modules when doing so reduces UI duplication, but it should not move DOM concerns into them.

## UI Cleanup Allowed During Refactor

The user explicitly allowed cleanup while refactoring. The allowed cleanup scope is:

- rename DOM hooks and classes to match clearer controller/view boundaries
- simplify repeated shell and workspace markup
- remove overly global element lookups
- normalize action-row and status-panel naming
- reduce incidental coupling between workspaces

The refactor should not use cleanup as a pretext for redesigning user-facing behavior that is unrelated to maintainability.

## Migration Strategy

The refactor will happen in safe slices.

### Slice 1: Bootstrap, shared state, and DOM helpers

Deliverables:

- introduce `bootstrap.ts`
- introduce `state/` and `dom/` helpers
- shrink `main.ts` by moving non-workspace helpers first

Expected result:

- no visible behavior change
- app still boots identically

### Slice 2: Shell and carousel extraction

Deliverables:

- extract shell navigation and top-bar control handling
- extract feature carousel state/behavior

Expected result:

- shell markup still renders from `app.ts`
- shell interactions are no longer wired directly inside `main.ts`

### Slice 3: Network workspace extraction

Deliverables:

- extract provider refresh
- extract live delivery actions
- extract observed-peer/timeline rendering

Expected result:

- network behavior is isolated and testable
- `main.ts` no longer owns delivery orchestration directly

### Slice 4: Store and directory extraction

Deliverables:

- extract marketplace refresh/render/search behavior
- extract directory panel rendering

Expected result:

- store and directory stop depending on shared free functions in `main.ts`

### Slice 5: Frontend workspace and wallet extraction

Deliverables:

- extract frontend workspace controller
- isolate Monaco/editor orchestration
- extract wallet/account modal handling into a dedicated controller

Expected result:

- frontend workspace is independently readable
- wallet flow is no longer shell-wired ad hoc

### Slice 6: Naming cleanup and final simplification

Deliverables:

- final renames of DOM hooks and helper APIs
- delete dead compatibility glue
- reduce `main.ts` to the minimal entry/bootstrap handoff

Expected result:

- clear module boundaries
- lower risk for future feature work

## Testing Strategy

The refactor requires behavior-preserving verification after each slice.

### Automated

- keep existing UI shell and runtime tests passing
- add focused tests for new controller/view helper modules where practical
- continue running:
  - `cd sdn-js && npx vitest run ...`
  - `cd sdn-js && npm run build:ui`

### Local Runtime Verification

- run `npm run admin:dev`
- verify the Vite-served `/admin` UI still talks to the local `sdn-server`
- verify the local node still bootstraps to the live remote peer
- verify the network, store, directory, frontend, and wallet flows still mount

## Risks

### DOM Hook Breakage

Renaming hooks and redistributing event binding can silently break interactions if view/controller boundaries drift.

Mitigation:

- move one workspace at a time
- keep smoke tests around shell markup and bootstrap behavior

### Over-Abstracting the UI

There is a risk of replacing one large file with many thin but unnecessary wrapper layers.

Mitigation:

- only extract modules with a clear ownership boundary
- avoid inventing a mini-framework

### Frontend Workspace Complexity

The Monaco/editor workflow is the highest-risk workspace because it has lifecycle and async state.

Mitigation:

- leave the editor wrapper itself simple
- move controller logic around it incrementally

## Definition of Done

This refactor is done when:

- the admin UI remains plain TypeScript + CSS with Vite outputting JavaScript and CSS assets
- `sdn-js/ui/src/main.ts` is reduced to a thin entry/bootstrap file
- shell, network, store, directory, frontend, and wallet concerns each have focused controller ownership
- reusable DOM helpers and shared app state exist outside workspace controllers
- `/admin` remains functional in both local dev and hosted server mode
- tests and `build:ui` pass after the refactor
