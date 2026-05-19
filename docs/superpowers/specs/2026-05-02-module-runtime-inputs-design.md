# Module Runtime Inputs Design

## Goal

Fix the Modules dashboard selection reset, make module method inputs and outputs readable in an OpenAPI-style layout, and let operators save runtime input values that put a module into an updated state until it is restarted.

## Scope

This change is limited to `space-data-network`.

- The SDN server owns pending module input values and command history.
- The SDN browser UI renders saved values, pending state, method schemas, and history.
- Canonical schema definitions remain upstream in SDS. This change does not add repo-local FlatBuffer schemas.

## Runtime Model

The plugin manager stores input values per module, method, and port. A saved value includes the selected wire format, encoding, schema metadata copied from the manifest, the value string, and an update timestamp. Saving values marks the module `restartPending`; runtime snapshots surface that as an `updated` module state with a status message telling the user to restart.

Command history records input saves and lifecycle restarts. The history is capped to keep snapshots bounded. If the manager has a runtime data path, the pending input values and history are persisted under the node data directory.

Restart applies pending inputs through an optional runtime interface. Generic modules can implement that interface without changing the dashboard API. The first implementation stores and surfaces the values reliably; deeper FlatBuffer encoding remains tied to canonical SDS schema support rather than repo-local `.fbs` files.

## API

- `GET /api/v1/modules/runtime` includes `inputValues`, `restartPending`, and `commandHistory` on each module entry.
- `PATCH /api/v1/modules/runtime/{moduleId}/inputs` accepts `{"values":[...]}` and returns the saved values plus pending state.
- `GET /api/v1/modules/runtime/{moduleId}/history` returns command history for direct inspection.
- Existing option and action endpoints stay unchanged.

## UI

The Modules page keeps the selected module stable across polling refreshes unless that module disappears. The module list has a fixed header with a help button and SDK documentation links, plus its own scroll area. The detail panel has a fixed top action bar with colored lifecycle buttons and its own scroll area.

Methods render as operation blocks with input and output schema panels. Inputs are editable forms by method and port. Saving inputs records command history, updates the module snapshot, and enables restart as the apply step. Outputs are display-only metadata cards with schema, root type, file identifier, wire formats, and descriptions.

## Verification

Focused verification must cover:

- selection resolution keeps a valid selected module after refresh;
- runtime snapshots include pending input values, updated state, and history;
- input-save and history endpoints validate and return the expected JSON;
- UI source contracts include the fixed header/help links, independent scroll regions, action bar, input form, and command history;
- `sdn-js` focused tests and Go plugin/server tests pass.
