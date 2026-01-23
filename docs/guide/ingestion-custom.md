# Custom Converters

::: warning COMING SOON
Custom converter development is under active development. This documentation describes the planned workflow.
:::

## Overview

Build custom WASM plugins to convert your proprietary data formats into Space Data Standards FlatBuffers.

## Quick Start

### Prerequisites

- Rust toolchain with `wasm32-unknown-unknown` target
- Node.js 18+ (for SDK tools)

```bash
# Install Rust WASM target
rustup target add wasm32-unknown-unknown

# Install plugin SDK
npm install -g @spacedatanetwork/plugin-sdk
```

### Create a New Plugin

```bash
# Create plugin project
sdn-plugin create my-converter
cd my-converter

# Project structure:
# my-converter/
# ├── Cargo.toml
# ├── src/
# │   └── lib.rs
# ├── tests/
# │   └── test_transform.rs
# └── plugin.json
```

### Implement the Plugin

Edit `src/lib.rs`:

```rust
use sdn_plugin_sdk::prelude::*;

// Plugin metadata
#[plugin_info]
static INFO: PluginInfo = PluginInfo {
    name: "my-converter",
    version: "1.0.0",
    input_formats: &["myformat"],
    output_schema: "OMM.fbs",
};

// Main transform function
#[plugin_transform]
fn transform(input: &[u8]) -> Result<Vec<u8>, PluginError> {
    // Parse your input format
    let my_data = parse_my_format(input)?;

    // Build OMM FlatBuffer
    let mut builder = flatbuffers::FlatBufferBuilder::new();

    let omm = OMM::create(&mut builder, &OMMArgs {
        object_name: Some(builder.create_string(&my_data.name)),
        object_id: Some(builder.create_string(&my_data.id)),
        epoch: Some(builder.create_string(&my_data.epoch)),
        mean_motion: my_data.mean_motion,
        eccentricity: my_data.eccentricity,
        inclination: my_data.inclination,
        // ... more fields
        ..Default::default()
    });

    builder.finish(omm, None);
    Ok(builder.finished_data().to_vec())
}

fn parse_my_format(input: &[u8]) -> Result<MyData, PluginError> {
    // Your parsing logic here
    todo!()
}

struct MyData {
    name: String,
    id: String,
    epoch: String,
    mean_motion: f64,
    eccentricity: f64,
    inclination: f64,
}
```

### Build and Test

```bash
# Build the WASM module
sdn-plugin build

# Run tests
sdn-plugin test

# Package for distribution
sdn-plugin package
```

## Plugin SDK

### Cargo.toml

```toml
[package]
name = "my-converter"
version = "1.0.0"
edition = "2021"

[lib]
crate-type = ["cdylib"]

[dependencies]
sdn-plugin-sdk = "1.0"
flatbuffers = "24.3"

[profile.release]
opt-level = "z"
lto = true
```

### Available Crates

| Crate | Description |
|-------|-------------|
| `sdn-plugin-sdk` | Core SDK with macros and helpers |
| `sdn-schemas` | Pre-generated FlatBuffer types for all schemas |
| `sdn-flatc` | FlatBuffer utilities |

### Helper Functions

```rust
use sdn_plugin_sdk::prelude::*;

// Date/time parsing
let epoch = parse_epoch("2024-01-15T12:00:00Z")?;
let tle_epoch = parse_tle_epoch("24015.50000000")?;

// Unit conversion
let rad_per_sec = revs_per_day_to_rad_per_sec(15.49);
let degrees = radians_to_degrees(1.234);

// Validation
validate_norad_id("25544")?;
validate_cospar_id("1998-067A")?;

// Checksum
let valid = verify_tle_checksum(line1);
```

## Example: CSV to OMM Converter

```rust
use sdn_plugin_sdk::prelude::*;
use csv::ReaderBuilder;

#[plugin_info]
static INFO: PluginInfo = PluginInfo {
    name: "csv-to-omm",
    version: "1.0.0",
    input_formats: &["csv"],
    output_schema: "OMM.fbs",
};

#[plugin_transform]
fn transform(input: &[u8]) -> Result<Vec<u8>, PluginError> {
    let mut reader = ReaderBuilder::new()
        .has_headers(true)
        .from_reader(input);

    let mut builder = flatbuffers::FlatBufferBuilder::new();
    let mut records = Vec::new();

    for result in reader.records() {
        let record = result.map_err(|e| PluginError::Parse(e.to_string()))?;

        let omm = OMM::create(&mut builder, &OMMArgs {
            object_name: Some(builder.create_string(&record[0])),
            object_id: Some(builder.create_string(&record[1])),
            epoch: Some(builder.create_string(&record[2])),
            mean_motion: record[3].parse().unwrap_or(0.0),
            eccentricity: record[4].parse().unwrap_or(0.0),
            inclination: record[5].parse().unwrap_or(0.0),
            ..Default::default()
        });

        records.push(omm);
    }

    // Create OMM collection
    let omm_vec = builder.create_vector(&records);
    let collection = OMMCollection::create(&mut builder, &OMMCollectionArgs {
        records: Some(omm_vec),
    });

    builder.finish(collection, None);
    Ok(builder.finished_data().to_vec())
}
```

## Streaming Plugins

For large files, implement the streaming interface:

```rust
use sdn_plugin_sdk::prelude::*;

#[plugin_info]
static INFO: PluginInfo = PluginInfo {
    name: "streaming-converter",
    version: "1.0.0",
    input_formats: &["large"],
    output_schema: "OMM.fbs",
    capabilities: PluginCapabilities {
        streaming: true,
        ..Default::default()
    },
};

struct StreamState {
    buffer: Vec<u8>,
    records: Vec<OMM>,
}

static mut STATE: Option<StreamState> = None;

#[plugin_stream_init]
fn stream_init() -> Result<(), PluginError> {
    unsafe {
        STATE = Some(StreamState {
            buffer: Vec::new(),
            records: Vec::new(),
        });
    }
    Ok(())
}

#[plugin_stream_chunk]
fn stream_chunk(chunk: &[u8]) -> Result<(), PluginError> {
    unsafe {
        if let Some(state) = STATE.as_mut() {
            state.buffer.extend_from_slice(chunk);

            // Process complete records from buffer
            while let Some((record, remaining)) = try_parse_record(&state.buffer) {
                state.records.push(record);
                state.buffer = remaining;
            }
        }
    }
    Ok(())
}

#[plugin_stream_finish]
fn stream_finish() -> Result<Vec<u8>, PluginError> {
    unsafe {
        if let Some(state) = STATE.take() {
            // Build final FlatBuffer from collected records
            build_flatbuffer(state.records)
        } else {
            Err(PluginError::Internal("Stream not initialized".into()))
        }
    }
}
```

## Testing

### Unit Tests

```rust
#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_transform_valid_input() {
        let input = include_bytes!("../tests/fixtures/valid.myformat");
        let output = transform(input).unwrap();

        // Verify output is valid FlatBuffer
        let omm = flatbuffers::root::<OMM>(&output).unwrap();
        assert_eq!(omm.object_name(), Some("ISS"));
    }

    #[test]
    fn test_transform_invalid_input() {
        let input = b"invalid data";
        let result = transform(input);
        assert!(result.is_err());
    }
}
```

### Integration Tests

Create `tests/test_transform.rs`:

```rust
use sdn_plugin_sdk::testing::*;

#[test]
fn test_end_to_end() {
    let plugin = load_plugin("target/wasm32-unknown-unknown/release/my_converter.wasm");

    let input = std::fs::read("tests/fixtures/input.myformat").unwrap();
    let output = plugin.transform(&input).unwrap();

    // Validate against schema
    validate_flatbuffer("OMM.fbs", &output).unwrap();
}
```

## Distribution

### Plugin Manifest

`plugin.json`:

```json
{
  "name": "my-converter",
  "version": "1.0.0",
  "description": "Converts MyFormat to OMM",
  "author": "Your Name",
  "license": "MIT",
  "repository": "https://github.com/yourname/my-converter",
  "inputFormats": ["myformat"],
  "outputSchema": "OMM.fbs",
  "wasmFile": "my_converter.wasm",
  "wasmSize": 45678,
  "sri": "sha384-abc123..."
}
```

### Publishing

```bash
# Package the plugin
sdn-plugin package

# Publish to registry (future)
sdn-plugin publish

# Or distribute the .wasm file directly
```

### Loading Custom Plugins

```typescript
// JavaScript - from URL
const plugin = await PluginLoader.load('https://example.com/my-converter.wasm');

// JavaScript - from file
const plugin = await PluginLoader.load('./plugins/my-converter.wasm');

// Go - from path
plugin, err := ingestion.LoadPlugin("./plugins/my-converter.wasm")
```

## Best Practices

1. **Validate inputs thoroughly** - Don't trust external data
2. **Handle partial data** - Support incomplete or malformed inputs gracefully
3. **Use streaming** - For files > 1MB, implement streaming interface
4. **Test edge cases** - Empty input, huge input, malformed data
5. **Document your format** - Include format specification in README
6. **Version compatibility** - Use semver for plugin versions

## Next Steps

- [Plugin Architecture](/guide/ingestion-plugins) - Technical details
- [Pipeline Overview](/guide/ingestion-overview) - Full system architecture
- [Schema Reference](/reference/schemas) - Target FlatBuffer schemas
