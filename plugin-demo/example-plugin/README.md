# Example SDN Plugin

Annotated reference implementation of an SDN WASM plugin.

## Prerequisites

- [wasi-sdk](https://github.com/WebAssembly/wasi-sdk/releases) installed at `/opt/wasi-sdk`

## Build

```bash
make
# or with custom wasi-sdk path:
make WASI_SDK=/path/to/wasi-sdk
```

## Required WASM Exports

Every SDN plugin **must** export these 7 functions:

| Function | Purpose |
|----------|---------|
| `malloc(size) → ptr` | Memory allocation for host↔guest data transfer |
| `free(ptr)` | Memory deallocation |
| `plugin_init(seed_ptr, seed_len) → status` | One-time init with identity seed |
| `plugin_handle_request(req_ptr, req_len, out_ptr, out_cap) → bytes_written` | Process incoming request |
| `plugin_get_public_key(out_ptr, out_cap) → bytes_written` | Return public key |
| `plugin_get_metadata(out_ptr, out_cap) → bytes_written` | Return JSON metadata |
| `plugin_request_challenge(req_ptr, req_len, out_ptr, out_cap) → bytes_written` | Sign challenge |

## Host Functions Available

| Function | Module | Purpose |
|----------|--------|---------|
| `clock_now_ms() → i64` | `sdn` | Current epoch milliseconds |
| `random_bytes(ptr, len) → i32` | `sdn` | Cryptographic random (max 8192) |
| `log(level, ptr, len)` | `sdn` | Write to daemon log |

## Memory Model

- Maximum: 512 WASM pages = 32 MB
- Host allocates via `malloc()` before passing data to plugin
- Plugin writes output into host-allocated buffer at `out_ptr`
- Host calls `free()` after reading output
- All data transfer is FlatBuffer binary format

## Plugin Metadata JSON

The `plugin_get_metadata` export must return JSON like:

```json
{
  "id": "example-sensor-plugin",
  "version": "1.0.0",
  "name": "Example Sensor Data Plugin",
  "description": "Demonstrates the SDN WASM plugin API",
  "protocols": ["/example/sensor-data/1.0.0"],
  "capabilities": ["publish", "subscribe"]
}
```

Fields:
- `id` — unique plugin identifier (used in API routes)
- `version` — semver version string
- `name` — display name
- `protocols` — libp2p protocol IDs to register
- `capabilities` — what the plugin can do

## Files

- `plugin.c` — Fully annotated source with all required exports
- `Makefile` — Build with wasi-sdk
