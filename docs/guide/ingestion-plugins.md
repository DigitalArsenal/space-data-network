# WASM Ingestion Plugins

::: warning COMING SOON
The WASM plugin system is under active development. This documentation describes the planned architecture.
:::

## Overview

SDN ingestion plugins are WebAssembly modules that transform raw data formats into FlatBuffer schemas. They provide a secure, portable, and efficient way to extend SDN's data processing capabilities.

## Plugin Architecture

### Memory Model

Plugins use a shared memory model for efficient data transfer:

```
┌─────────────────────────────────────────────────────────────────┐
│                      WASM Linear Memory                        │
├──────────┬──────────┬──────────┬──────────┬───────────────────┤
│  Stack   │  Heap    │  Input   │  Output  │    Reserved       │
│          │          │  Buffer  │  Buffer  │                   │
│  0x0000  │  0x1000  │  0x8000  │  0xC000  │     0xFFFF        │
└──────────┴──────────┴──────────┴──────────┴───────────────────┘
```

### Export Interface

Every plugin must export these functions:

```c
// Plugin metadata
extern const char* get_name();
extern const char* get_version();
extern const char* get_input_formats();  // comma-separated
extern const char* get_output_schema();

// Core transformation
extern int32_t transform(
    const uint8_t* input,
    uint32_t input_len,
    uint8_t* output,
    uint32_t output_capacity
);

// Optional: streaming interface
extern int32_t transform_stream_init();
extern int32_t transform_stream_chunk(const uint8_t* chunk, uint32_t len);
extern int32_t transform_stream_finish(uint8_t* output, uint32_t capacity);

// Memory management
extern void* allocate(uint32_t size);
extern void deallocate(void* ptr, uint32_t size);
```

### Return Codes

| Code | Meaning |
|------|---------|
| `>= 0` | Success, returns bytes written |
| `-1` | Invalid input format |
| `-2` | Output buffer too small |
| `-3` | Validation error |
| `-4` | Internal error |

## Plugin Lifecycle

### Loading

```typescript
// JavaScript
import { PluginLoader } from '@spacedatanetwork/sdn-js';

const plugin = await PluginLoader.load('./tle-to-omm.wasm');

console.log(plugin.name);         // "tle-to-omm"
console.log(plugin.version);      // "1.0.0"
console.log(plugin.inputFormats); // ["tle", "3le"]
console.log(plugin.outputSchema); // "OMM.fbs"
```

```go
// Go
import "github.com/spacedatanetwork/sdn-server/internal/ingestion"

plugin, err := ingestion.LoadPlugin("./tle-to-omm.wasm")
if err != nil {
    log.Fatal(err)
}

fmt.Println(plugin.Name())         // "tle-to-omm"
fmt.Println(plugin.OutputSchema()) // "OMM.fbs"
```

### Execution

```typescript
// JavaScript - single transform
const output = await plugin.transform(inputBytes);

// JavaScript - streaming
const stream = plugin.createStream();
for await (const chunk of inputStream) {
    stream.write(chunk);
}
const output = await stream.finish();
```

```go
// Go - single transform
output, err := plugin.Transform(inputBytes)

// Go - streaming
stream := plugin.NewStream()
for chunk := range inputChan {
    stream.Write(chunk)
}
output, err := stream.Finish()
```

### Unloading

Plugins are automatically unloaded when no longer referenced. For explicit cleanup:

```typescript
plugin.dispose();
```

```go
plugin.Close()
```

## Security Model

### Sandboxing

Plugins run in a sandboxed WASM environment with:

- **No filesystem access** - Can't read or write files
- **No network access** - Can't make HTTP requests
- **No system calls** - Can't execute shell commands
- **Memory limits** - Configurable max memory (default 64MB)
- **CPU limits** - Configurable execution timeout (default 30s)

### Capabilities

Plugins can request optional capabilities:

```json
{
  "name": "my-plugin",
  "capabilities": {
    "env_vars": ["TZ"],           // Read specific env vars
    "random": true,                // Access to random numbers
    "clock": true                  // Access to wall clock
  }
}
```

The host decides whether to grant these capabilities.

### Verification

Plugins can be signed for integrity verification:

```bash
# Sign a plugin
sdn-plugin sign --key private.pem my-plugin.wasm

# Verify before loading
sdn-plugin verify --pubkey public.pem my-plugin.wasm
```

## Built-in Plugins

### TODO: TLE to OMM (`tle-to-omm.wasm`)

Converts Two-Line Element sets to OMM FlatBuffers.

**Input Formats:**
- Standard TLE (2 lines)
- 3LE (3 lines with name)
- JSON TLE

**Features:**
- Checksum validation
- Epoch conversion (TLE epoch → ISO 8601)
- Unit conversion (revs/day → rad/s)
- B* drag term handling

**Example:**
```typescript
const plugin = await PluginLoader.load('builtin:tle-to-omm');

const tleData = `
ISS (ZARYA)
1 25544U 98067A   24015.50000000  .00001234  00000-0  12345-4 0  9999
2 25544  51.6400 123.4500 0001234  67.8900 234.5600 15.49000000123456
`.trim();

const omm = await plugin.transform(new TextEncoder().encode(tleData));
// omm is now a valid OMM FlatBuffer binary
```

### TODO: CCSDS XML (`ccsds-xml.wasm`)

Converts CCSDS Navigation Data Messages XML to native FlatBuffers.

**Supported Message Types:**
- OPM → OMM.fbs
- OEM → OEM.fbs
- CDM → CDM.fbs
- TDM → TDM.fbs

### TODO: SP3 to OEM (`sp3-to-oem.wasm`)

Converts SP3 precise ephemeris files to OEM FlatBuffers.

### TODO: Generic Mapper (`generic-mapper.wasm`)

Configurable CSV/JSON to any schema mapper.

## Performance

### Benchmarks

| Plugin | Input Size | Transform Time | Memory |
|--------|-----------|----------------|--------|
| tle-to-omm | 1 KB | < 1 ms | 2 MB |
| tle-to-omm | 1 MB | ~50 ms | 4 MB |
| ccsds-xml | 10 KB | ~5 ms | 8 MB |
| ccsds-xml | 1 MB | ~100 ms | 32 MB |

### Optimization Tips

1. **Use streaming** for large files
2. **Batch multiple** records in one transform
3. **Pre-allocate** output buffers
4. **Reuse** plugin instances

## Debugging

### Logging

Plugins can emit debug logs via the `log` import:

```rust
#[link(wasm_import_module = "env")]
extern "C" {
    fn log_debug(ptr: *const u8, len: u32);
    fn log_info(ptr: *const u8, len: u32);
    fn log_warn(ptr: *const u8, len: u32);
    fn log_error(ptr: *const u8, len: u32);
}
```

Enable plugin logging:

```typescript
const plugin = await PluginLoader.load('tle-to-omm.wasm', {
    logLevel: 'debug'
});
```

### Error Handling

```typescript
try {
    const output = await plugin.transform(input);
} catch (err) {
    if (err instanceof PluginValidationError) {
        console.error('Invalid input:', err.details);
    } else if (err instanceof PluginTimeoutError) {
        console.error('Transform timed out');
    } else {
        throw err;
    }
}
```

## Next Steps

- [Custom Converters](/guide/ingestion-custom) - Build your own plugins
- [Pipeline Overview](/guide/ingestion-overview) - Full ingestion architecture
- [Schema Reference](/reference/schemas) - Target schema specifications
