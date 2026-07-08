# Plugin Demo Integration Tests

End-to-end tests for the SDN data flow including server startup, FlatBuffer
publishing, PNM notifications, and record retrieval verification.

## Prerequisites

- **Go 1.21+** — for building the SDN server
- **Node.js 18+** — for running tests (uses native `fetch`)

## Quick Start

```bash
# Install deps and run
npm install
npm test

# Or use the runner script (also builds server)
bash run.sh

# Verbose mode (shows server logs)
SDN_TEST_VERBOSE=1 npm test
# or
bash run.sh --verbose
```

## What's Tested

| Test | Description |
|------|-------------|
| Server Health | Node info, catalog endpoints |
| Publish Data | POST FlatBuffer PNM and OMM messages |
| Query Data | GET published records back by schema and CID |
| Record History | Published record history endpoints |
| FlatBuffer Format | Binary layout, file identifiers, round-trip |
| Batch Publish | Multi-record batch publish |
| Node API | Schema list, plugin manifest |

## How It Works

1. **Build** — Compiles the SDN server binary from `sdn-server/`
2. **Configure** — Creates a temp config with:
   - Ephemeral ports (no conflicts)
   - Auth disabled (`require_auth: false`)
   - TOR disabled
   - Schema validation disabled (no flatc WASM needed)
   - Publishing enabled for all schemas
3. **Start** — Launches the server process, waits for readiness
4. **Test** — Exercises REST API endpoints with FlatBuffer payloads
5. **Cleanup** — Kills server, removes temp directory

## Running from CI

These tests are included in the CI pipeline via `scripts/ci-local.sh`:

```bash
./scripts/ci-local.sh plugin-demo
# or as part of quick/full runs:
./scripts/ci-local.sh quick
```

## Files

| File | Purpose |
|------|---------|
| `integration.test.mjs` | Main test suite |
| `helpers/test-server.mjs` | Server build/launch/cleanup |
| `package.json` | Test dependencies (flatbuffers) |
| `run.sh` | Standalone test runner script |
