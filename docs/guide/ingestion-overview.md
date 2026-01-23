# Data Ingestion Pipeline Overview

::: warning COMING SOON
Data ingestion pipelines are under active development. This documentation describes the planned architecture and plugin system.
:::

## Introduction

Space Data Network includes a powerful data ingestion system that converts raw data formats into standardized FlatBuffer schemas. The system is built on WebAssembly (WASM) plugins that run in **every SDN runtime** - server, browser, and edge devices.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     Data Ingestion Pipeline                             │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌─────────────┐     ┌──────────────────┐     ┌─────────────────┐     │
│  │  Raw Data   │────►│   WASM Plugin    │────►│   FlatBuffer    │     │
│  │             │     │                  │     │                 │     │
│  │  - TLE      │     │  ┌────────────┐  │     │  - OMM.fbs      │     │
│  │  - XML      │     │  │ Transform  │  │     │  - CDM.fbs      │     │
│  │  - CSV      │     │  │ Validate   │  │     │  - OEM.fbs      │     │
│  │  - JSON     │     │  │ Normalize  │  │     │  - etc.         │     │
│  │  - Binary   │     │  └────────────┘  │     │                 │     │
│  └─────────────┘     └──────────────────┘     └─────────────────┘     │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

## Why WASM Plugins?

### Cross-Platform Compatibility

WASM plugins run identically in:
- **Go server** (via wazero runtime)
- **Browser** (native WebAssembly)
- **Node.js** (WASM support)
- **Edge devices** (embedded WASM runtimes)

### Security

- **Sandboxed execution** - Plugins can't access system resources
- **Memory isolation** - Each plugin has its own memory space
- **Capability-based** - Explicit permissions for I/O operations

### Performance

- **Near-native speed** - WASM compiles to efficient machine code
- **Zero-copy** - Direct memory access for FlatBuffer output
- **Streaming** - Process data without loading entire files

## Architecture

### Plugin Interface

Each ingestion plugin implements a standard interface:

```rust
// Rust plugin example (compiled to WASM)
#[no_mangle]
pub extern "C" fn get_plugin_info() -> *const PluginInfo {
    &PluginInfo {
        name: "tle-to-omm",
        version: "1.0.0",
        input_formats: &["tle", "3le"],
        output_schema: "OMM.fbs",
    }
}

#[no_mangle]
pub extern "C" fn transform(
    input_ptr: *const u8,
    input_len: u32,
    output_ptr: *mut u8,
    output_capacity: u32,
) -> i32 {
    // Transform TLE to OMM FlatBuffer
    // Returns bytes written or negative error code
}
```

### Plugin Registry

SDN maintains a registry of available plugins:

```typescript
// JavaScript API
import { PluginRegistry } from '@spacedatanetwork/sdn-js';

// List available plugins
const plugins = await PluginRegistry.list();
// [
//   { name: 'tle-to-omm', inputFormats: ['tle', '3le'], outputSchema: 'OMM.fbs' },
//   { name: 'ccsds-xml-to-omm', inputFormats: ['xml'], outputSchema: 'OMM.fbs' },
//   { name: 'sp3-to-oem', inputFormats: ['sp3'], outputSchema: 'OEM.fbs' },
// ]

// Load a plugin
const tlePlugin = await PluginRegistry.load('tle-to-omm');
```

### Processing Pipeline

```
┌──────────────────────────────────────────────────────────────────────┐
│                       Ingestion Pipeline                             │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌──────┐    ┌────────┐    ┌──────────┐    ┌────────┐    ┌──────┐ │
│  │Input │───►│Detect  │───►│Transform │───►│Validate│───►│Output│ │
│  │      │    │Format  │    │(WASM)    │    │Schema  │    │      │ │
│  └──────┘    └────────┘    └──────────┘    └────────┘    └──────┘ │
│                                                                      │
│                    ▲               │                                 │
│                    │               │                                 │
│               ┌────┴────┐    ┌────┴─────┐                           │
│               │ Plugin  │    │ Error    │                           │
│               │ Registry│    │ Handler  │                           │
│               └─────────┘    └──────────┘                           │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

## Planned Plugins

### TLE to OMM

Convert Two-Line Element sets to Orbital Mean-Elements Messages.

| Property | Value |
|----------|-------|
| Input Formats | `.tle`, `.3le`, `.txt` |
| Output Schema | `OMM.fbs` |
| Status | 🚧 In Development |

```
Input (TLE):
ISS (ZARYA)
1 25544U 98067A   24015.50000000  .00001234  00000-0  12345-4 0  9999
2 25544  51.6400 123.4500 0001234  67.8900 234.5600 15.49000000123456

Output (OMM FlatBuffer):
{
  "OBJECT_NAME": "ISS (ZARYA)",
  "OBJECT_ID": "1998-067A",
  "EPOCH": "2024-01-15T12:00:00.000Z",
  "MEAN_MOTION": 15.49,
  "ECCENTRICITY": 0.0001234,
  "INCLINATION": 51.64,
  ...
}
```

### CCSDS XML to FlatBuffer

Convert CCSDS Navigation Data Messages (NDM) XML to native FlatBuffers.

| Property | Value |
|----------|-------|
| Input Formats | `.xml` |
| Output Schemas | `OMM.fbs`, `OEM.fbs`, `CDM.fbs`, `TDM.fbs` |
| Status | 📋 Planned |

### SP3 to OEM

Convert SP3 precise ephemeris files to Orbit Ephemeris Messages.

| Property | Value |
|----------|-------|
| Input Formats | `.sp3`, `.sp3c`, `.sp3d` |
| Output Schema | `OEM.fbs` |
| Status | 📋 Planned |

### RINEX to TDM

Convert RINEX observation files to Tracking Data Messages.

| Property | Value |
|----------|-------|
| Input Formats | `.obs`, `.rnx` |
| Output Schema | `TDM.fbs` |
| Status | 📋 Planned |

### CSV/JSON Generic Mapper

Configurable mapper for CSV and JSON data sources.

| Property | Value |
|----------|-------|
| Input Formats | `.csv`, `.json` |
| Output Schemas | Any (configurable) |
| Status | 📋 Planned |

## Usage Examples

### JavaScript/Browser

```typescript
import { SDNNode, IngestPipeline } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Create ingestion pipeline
const pipeline = new IngestPipeline();

// Ingest TLE data
const tleData = `ISS (ZARYA)
1 25544U 98067A   24015.50000000  .00001234  00000-0  12345-4 0  9999
2 25544  51.6400 123.4500 0001234  67.8900 234.5600 15.49000000123456`;

const omm = await pipeline.ingest(tleData, {
  inputFormat: 'tle',
  outputSchema: 'OMM',
});

// Publish the converted data
await node.publish('OMM', omm);
```

### Go Server

```go
package main

import (
    "github.com/spacedatanetwork/sdn-server/internal/ingestion"
)

func main() {
    // Load TLE plugin
    plugin, _ := ingestion.LoadPlugin("tle-to-omm")

    // Convert TLE to OMM
    tleData := []byte("ISS (ZARYA)\n1 25544U ...")
    ommBytes, _ := plugin.Transform(tleData)

    // ommBytes is now a valid OMM FlatBuffer
}
```

### CLI

```bash
# Convert TLE file to OMM
spacedatanetwork ingest --input iss.tle --format tle --schema OMM --output iss.omm

# Batch convert directory
spacedatanetwork ingest --input ./tle-files/ --format tle --schema OMM --output ./omm-files/

# Ingest and publish directly
spacedatanetwork ingest --input iss.tle --format tle --publish
```

## Plugin Development

### Creating a Custom Plugin

Plugins can be written in any language that compiles to WASM:
- Rust (recommended)
- C/C++
- AssemblyScript
- Go (TinyGo)

See [Custom Converters](/guide/ingestion-custom) for a detailed guide.

### Plugin SDK

```bash
# Install the plugin SDK
npm install @spacedatanetwork/plugin-sdk

# Create a new plugin project
npx sdn-plugin create my-converter
cd my-converter

# Build the plugin
npm run build

# Test locally
npm test

# Package for distribution
npm run package
```

## Roadmap

| Phase | Features | Status |
|-------|----------|--------|
| **Phase 1** | Plugin architecture, TLE-to-OMM | 🚧 In Development |
| **Phase 2** | CCSDS XML converters, SP3 support | 📋 Planned |
| **Phase 3** | Custom mapper tools, plugin registry | 📋 Planned |
| **Phase 4** | Streaming ingestion, batch processing | 📋 Planned |

## Next Steps

- [WASM Plugins](/guide/ingestion-plugins) - Technical details on the plugin system
- [Custom Converters](/guide/ingestion-custom) - Build your own converter plugins
- [Schema Reference](/reference/schemas) - Target schema specifications
