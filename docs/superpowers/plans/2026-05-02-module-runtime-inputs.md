# Module Runtime Inputs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix Modules dashboard selection stability and add server-backed runtime input drafts, updated state, restart apply flow, and command history.

**Architecture:** Extend the SDN plugin manager runtime snapshot with pending input values and command history, then expose a focused inputs/history API. Update the React override page to render an OpenAPI-style method panel with editable input forms and independent scrolling panels.

**Tech Stack:** Go `sdn-server` plugin manager and CLI HTTP handlers, TypeScript `sdn-js` runtime helpers with Vitest, React 16 override UI in `sdn-js/ui`.

---

### File Map

- Modify `sdn-server/plugins/manager.go`: runtime input value types, pending state, history, persistence, restart apply hook, snapshot fields.
- Modify `sdn-server/plugins/manager_test.go`: manager tests for saving inputs, updated state, restart clearing pending state, and history.
- Modify `sdn-server/cmd/spacedatanetwork/main.go`: route parsing and handlers for module input save/history reads.
- Modify `sdn-server/cmd/spacedatanetwork/main_test.go`: HTTP tests for the new input and history endpoints.
- Modify `sdn-js/src/ui/runtime/modules.ts`: TypeScript types, normalization, selection helper, input-save client helper.
- Modify `sdn-js/src/ui/runtime/modules.test.ts`: runtime helper and normalization tests.
- Modify `sdn-js/src/ui/upstream-webui/branding.test.ts`: source contracts for Modules page layout and controls.
- Modify `sdn-js/ui/src/upstream-webui/overrides/modules/ModulesPage.js`: selection fix, fixed/scrolling layout, help menu, lifecycle action bar, OpenAPI-style methods, input form, command history.
- Modify `docs/module-runtime-dashboard.md`: document pending inputs and command history.

### Tasks

- [x] Add failing Go manager tests for pending input values and command history.
- [x] Implement manager input state, snapshot fields, restart clearing, and bounded history.
- [x] Add failing Go HTTP handler tests for `inputs` and `history`.
- [x] Implement route parsing and input/history endpoints.
- [x] Add failing Vitest coverage for runtime helper types, normalization, selection resolution, and input-save requests.
- [x] Implement `sdn-js` helper and normalization changes.
- [x] Add failing UI source contracts for fixed panels, help links, input forms, command history, and selection refresh behavior.
- [x] Implement Modules page layout and behavior changes.
- [x] Update module runtime dashboard docs.
- [x] Run focused Go tests and `sdn-js` focused Vitest tests.
- [x] Run `npm run build:ui` for UI build verification.
- [x] Run required stack status checks without recursive submodule commands.
