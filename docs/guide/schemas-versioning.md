# Schemas and Versioning

Space Data Network implements all 32 [Space Data Standards](https://spacedatastandards.org) schemas using [FlatBuffers](https://google.github.io/flatbuffers/) for efficient, zero-copy serialization. This guide covers the standards, how schemas are managed, and versioning strategies.

## Space Data Standards Overview

### What are Space Data Standards?

Space Data Standards (SDS) is a comprehensive set of schemas designed for exchanging space-related data between organizations, systems, and applications. The standards define precise data formats for everything from orbital elements to conjunction warnings to entity identification.

### Origin and Governance

The Space Data Standards are maintained at [spacedatastandards.org](https://spacedatastandards.org) and draw heavily from:

- **CCSDS Standards** - Consultative Committee for Space Data Systems recommendations
- **Industry Best Practices** - Formats used by major space agencies and operators
- **Community Input** - Open contribution process for improvements

The standards are designed to be:
- **Machine-readable** - Defined in FlatBuffers IDL (Interface Definition Language)
- **Self-documenting** - Include inline documentation and metadata
- **Versioned** - Support evolution without breaking compatibility

### Why Standardization Matters

| Challenge | How SDS Helps |
|-----------|---------------|
| **Data fragmentation** | Single format for all participants |
| **Integration costs** | Parse once, use everywhere |
| **Validation** | Schema-based verification |
| **Interoperability** | Cross-platform, cross-language support |
| **Future-proofing** | Built-in versioning and evolution |

## Supported Schemas (32 Total)

SDN supports all 32 Space Data Standards schemas, organized into functional categories:

### Orbital Data

| Schema | Name | Description |
|--------|------|-------------|
| **OMM** | Orbit Mean-Elements Message | Mean orbital elements compatible with TLE/3LE data. The most common format for sharing satellite positions. |
| **OEM** | Orbit Ephemeris Message | Time-series of state vectors (position/velocity) for high-precision orbit representation. |
| **OCM** | Orbit Comprehensive Message | Complete orbit characterization including covariance and maneuver history. |
| **OSM** | Orbit State Message | Instantaneous state vector at a specific epoch. |

### Conjunction Assessment

| Schema | Name | Description |
|--------|------|-------------|
| **CDM** | Conjunction Data Message | Close approach predictions between space objects including collision probability. |
| **CSM** | Conjunction Summary Message | Brief summary of conjunction events for rapid assessment. |

### Tracking Data

| Schema | Name | Description |
|--------|------|-------------|
| **TDM** | Tracking Data Message | Radar and optical observations from ground stations per CCSDS 503.0-B-1. |
| **RFM** | Reference Frame Message | Coordinate frame definitions and transformations. |

### Catalog and Entity

| Schema | Name | Description |
|--------|------|-------------|
| **CAT** | Catalog Entry | Space object catalog information (NORAD ID, object type, owner, orbital regime). |
| **SIT** | Site Message | Ground station and facility information. |
| **EPM** | Entity Profile Message | Organization identity, contact information, and cryptographic keys. |
| **PNM** | Peer Network Manifest | Network node identity and capabilities. |

### Maneuver Data

| Schema | Name | Description |
|--------|------|-------------|
| **MET** | Maneuver Execution Tracking | Records of executed maneuvers. |
| **MPE** | Maneuver Planning Ephemeris | Planned maneuver details for coordination. |

### Propagation and Reference

| Schema | Name | Description |
|--------|------|-------------|
| **HYP** | Hyperbolic Orbit | Hyperbolic trajectory parameters for escape/capture scenarios. |
| **EME** | Electromagnetic Emissions | RF and electromagnetic signature data. |
| **EOO** | Earth Orientation | Earth orientation parameters. |
| **EOP** | Earth Orientation Parameters | Polar motion and UT1-UTC corrections for precise transformations. |

### Reference and Launch

| Schema | Name | Description |
|--------|------|-------------|
| **LCC** | Launch Collision Corridor | Launch trajectory constraints for deconfliction. |
| **LDM** | Launch Data Message | Launch event information and parameters. |
| **CRM** | Collision Risk Message | Collision probability and risk assessments. |
| **CTR** | Contact Report | Communication contact records. |

### Additional Standards

| Schema | Name | Description |
|--------|------|-------------|
| **ATM** | Attitude Message | Spacecraft attitude (orientation) information. |
| **BOV** | Body Orientation and Velocity | Combined attitude and angular velocity. |
| **IDM** | Initial Data Message | Initial orbit determination data. |
| **PLD** | Payload Message | Spacecraft payload information. |
| **PRG** | Propagation Settings | Orbit propagation configuration. |
| **REC** | Record Message | Generic data records. |
| **ROC** | Re-entry Operations Corridor | Re-entry trajectory predictions. |
| **SCM** | Spacecraft Message | Spacecraft configuration and characteristics. |
| **TIM** | Time Message | Time synchronization data. |
| **VCM** | Vector Covariance Message | State vector with full covariance matrix. |

## Schema Registration System

### How Schemas are Embedded

SDN embeds all 32 schemas directly into both the Go server and JavaScript SDK, ensuring schemas are always available without external dependencies.

#### Go Server Embedding

The Go server uses the `//go:embed` directive to compile schemas into the binary:

```go
// From sdn-server/internal/sds/validator.go
//go:embed schemas/*.fbs
var schemasFS embed.FS

// SupportedSchemas lists all SDS schema files.
var SupportedSchemas = []string{
    "ATM.fbs",  // Attitude Message
    "BOV.fbs",  // Body Orientation and Velocity
    "CAT.fbs",  // Catalog
    "CDM.fbs",  // Conjunction Data Message
    // ... all 32 schemas
}
```

#### JavaScript SDK Bundling

The JavaScript SDK includes schema definitions as TypeScript constants:

```typescript
// From sdn-js/src/schemas.ts
export const SUPPORTED_SCHEMAS = [
  'ATM.fbs',   // Attitude Message
  'BOV.fbs',   // Body Orientation and Velocity
  'CAT.fbs',   // Catalog
  'CDM.fbs',   // Conjunction Data Message
  // ... all 32 schemas
] as const;

export type SchemaName = typeof SUPPORTED_SCHEMAS[number];
```

### Schema Registry Implementation

The `SchemaRegistry` manages schema loading, lookup, and metadata:

```go
// SchemaRegistry manages SDS schema files and metadata.
type SchemaRegistry struct {
    schemas      map[string][]byte // schema name -> content
    descriptions map[string]string // schema name -> description
    mu           sync.RWMutex
}

// NewSchemaRegistry creates a new schema registry with embedded schemas.
func NewSchemaRegistry() (*SchemaRegistry, error) {
    r := &SchemaRegistry{
        schemas:      make(map[string][]byte),
        descriptions: make(map[string]string),
    }

    // Load embedded schemas
    if err := r.loadEmbedded(); err != nil {
        log.Warnf("Failed to load embedded schemas: %v", err)
        r.loadDefaults()
    }

    return r, nil
}
```

Registry methods:

| Method | Description |
|--------|-------------|
| `Get(name)` | Retrieve schema content by name |
| `Has(name)` | Check if schema exists |
| `List()` | Return all schema names |
| `Info()` | Return metadata for all schemas |
| `Add(name, content, description)` | Register a custom schema |

### Adding Custom Schemas

While SDN includes all standard schemas, you can register custom schemas:

```go
// Go: Add a custom schema
registry, _ := sds.NewSchemaRegistry()
customSchema := []byte(`
/// Custom telemetry schema
table CustomTelemetry {
    TIMESTAMP: string;
    SENSOR_ID: string;
    VALUE: double;
    UNIT: string;
}
root_type CustomTelemetry;
file_identifier "$CTM";
`)
registry.Add("CUSTOM.fbs", customSchema, "Custom telemetry data")
```

```typescript
// TypeScript: Validate custom schema support
import { isValidSchema } from '@spacedatanetwork/sdn-js';

if (isValidSchema('OMM.fbs')) {
    // Schema is supported
}
```

## FlatBuffers Advantages

SDN uses [FlatBuffers](https://google.github.io/flatbuffers/) instead of JSON, Protocol Buffers, or other formats for several key reasons:

### Zero-Copy Deserialization

FlatBuffers allows accessing data directly from the serialized buffer without parsing:

```
Traditional Format:
  [Wire Data] → Parse → [Memory Allocation] → [Objects]

FlatBuffers:
  [Wire Data] → Access directly (no parsing/allocation)
```

This is critical for high-throughput space data streams where thousands of orbital updates may arrive per second.

### Cross-Platform Compatibility

FlatBuffers generates native code for multiple languages from the same schema:

| Language | Support |
|----------|---------|
| Go | Native via `flatc` |
| TypeScript/JavaScript | Native via `flatc` |
| C++ | Native via `flatc` |
| Python | Native via `flatc` |
| Rust | Native via `flatc` |
| Java/Kotlin | Native via `flatc` |

### Schema Evolution Support

FlatBuffers supports adding new fields without breaking existing readers:

```flatbuffers
// Version 1
table OMM {
    OBJECT_NAME: string;
    NORAD_CAT_ID: uint32;
    EPOCH: string;
    MEAN_MOTION: double;
}

// Version 2 - New field added (backward compatible)
table OMM {
    OBJECT_NAME: string;
    NORAD_CAT_ID: uint32;
    EPOCH: string;
    MEAN_MOTION: double;
    NEW_FIELD: double;  // Old readers ignore this
}
```

### Size Efficiency

FlatBuffers produces compact binary output compared to text formats:

| Format | Typical Size | Parse Time |
|--------|--------------|------------|
| JSON | Baseline (100%) | Baseline |
| FlatBuffers | ~50-70% | ~10x faster |

## Versioning Strategy

### Schema File Versioning

Each schema file includes version metadata in the header:

```flatbuffers
// Hash: 4052bd4e7b1e02b4ac22e3e908b4f71af8ff6849f6bde1a8d6f025d49ba8e2b3
// Version: 1.0.5
// -----------------------------------END_HEADER
```

The version format follows semantic versioning: `MAJOR.MINOR.PATCH`

### CCSDS Version Fields

Standard messages include a CCSDS version field for protocol-level versioning:

```flatbuffers
table OMM {
    /// CCSDS OMM Version
    CCSDS_OMM_VERS: double;
    // ...
}

table CDM {
    /// The version of the CCSDS CDM standard used
    CCSDS_CDM_VERS: double;
    // ...
}
```

### FlatBuffers Field Numbering

FlatBuffers uses implicit field numbering. Field order in the schema determines the binary layout:

```flatbuffers
table Example {
    FIELD_A: string;    // Field 0
    FIELD_B: int;       // Field 1
    FIELD_C: double;    // Field 2
}
```

::: warning Important
Never reorder existing fields. New fields must always be added at the end.
:::

### Adding New Fields Safely

To add a field without breaking compatibility:

1. **Add at the end** of the table definition
2. **Make it optional** (no `required` attribute)
3. **Provide a default** if meaningful

```flatbuffers
table OMM {
    // Existing fields...
    MEAN_MOTION: double;
    ECCENTRICITY: double;

    // New field - safe addition
    USER_DEFINED_EPOCH_TIMESTAMP: double;  // Optional, defaults to 0
}
```

### Deprecating Fields

Deprecated fields should be documented but not removed:

```flatbuffers
table Example {
    ACTIVE_FIELD: string;
    DEPRECATED_FIELD: string (deprecated);  // Keep for compatibility
    NEW_REPLACEMENT: string;  // Use this instead
}
```

### Breaking vs Non-Breaking Changes

| Change Type | Compatible? | Example |
|-------------|-------------|---------|
| Add optional field | Yes | Add new telemetry field |
| Add required field | **No** | Add mandatory identifier |
| Remove field | **No** | Delete deprecated field |
| Rename field | **No** | Change `OBJECT_ID` to `ID` |
| Change field type | **No** | Change `string` to `int` |
| Reorder fields | **No** | Move fields around |
| Add enum value | Yes | Add new object type |
| Remove enum value | **No** | Remove unused type |

## Schema Validation

### Validation on Receipt

All incoming data is validated against its declared schema before processing:

```go
// Validator validates data against SDS schemas.
type Validator struct {
    flatc   *wasm.FlatcModule
    schemas map[string]int // schema name -> schema ID
    mu      sync.RWMutex
}

// Validate validates data against a schema.
func (v *Validator) Validate(ctx context.Context, schemaName string, data []byte) error {
    v.mu.RLock()
    schemaID, ok := v.schemas[schemaName]
    v.mu.RUnlock()

    if !ok {
        return fmt.Errorf("unknown schema: %s", schemaName)
    }

    // Use WASM module to validate
    if v.flatc != nil {
        _, err := v.flatc.BinaryToJSON(ctx, schemaID, data)
        if err != nil {
            return fmt.Errorf("validation failed for %s: %w", schemaName, err)
        }
    }

    return nil
}
```

### Rejecting Invalid Data

Invalid data is rejected at multiple levels:

1. **Schema Check** - Verify schema exists and is supported
2. **Format Validation** - Verify data is valid FlatBuffer binary
3. **Field Validation** - Verify required fields are present
4. **Type Validation** - Verify field values match expected types

```go
// Example validation flow
err := validator.Validate(ctx, "OMM.fbs", incomingData)
if err != nil {
    log.Warnf("Rejecting invalid OMM data: %v", err)
    return // Don't process invalid data
}
```

### Error Handling

Common validation errors and handling:

| Error | Cause | Response |
|-------|-------|----------|
| `unknown schema` | Schema not registered | Reject, log warning |
| `validation failed` | Data doesn't match schema | Reject, log details |
| `empty data` | Zero-length payload | Reject |
| `WASM module not loaded` | Validator not initialized | Fallback to basic checks |

## Working with Schemas

### Listing Available Schemas

**Go:**

```go
import "github.com/spacedatanetwork/sdn-server/internal/sds"

// List all supported schemas
for _, schema := range sds.SupportedSchemas {
    fmt.Printf("%s - %s\n", schema, sds.GetDescription(schema))
}

// Using the registry
registry, _ := sds.NewSchemaRegistry()
for _, info := range registry.Info() {
    fmt.Printf("%s: %s (%d bytes)\n", info.Name, info.Description, info.Size)
}
```

**TypeScript:**

```typescript
import { SUPPORTED_SCHEMAS, SCHEMA_DESCRIPTIONS } from '@spacedatanetwork/sdn-js';

// List all schemas
SUPPORTED_SCHEMAS.forEach(schema => {
    console.log(`${schema}: ${SCHEMA_DESCRIPTIONS[schema]}`);
});
```

### Converting JSON to Binary

The WASM module handles JSON-to-FlatBuffer conversion:

```go
// Create WASM module
flatc, err := wasm.NewFlatcModule(ctx, "/path/to/flatc.wasm")
if err != nil {
    log.Fatal(err)
}
defer flatc.Close(ctx)

// Add schema
schemaID, err := flatc.AddSchema(ctx, "OMM.fbs", ommSchemaContent)
if err != nil {
    log.Fatal(err)
}

// Convert JSON to binary
jsonData := []byte(`{
    "OBJECT_NAME": "ISS (ZARYA)",
    "NORAD_CAT_ID": 25544,
    "EPOCH": "2024-01-15T12:00:00Z",
    "MEAN_MOTION": 15.5,
    "ECCENTRICITY": 0.0001
}`)

binaryData, err := flatc.JSONToBinary(ctx, schemaID, jsonData)
if err != nil {
    log.Fatalf("Conversion failed: %v", err)
}
```

### Converting Binary to JSON

```go
// Convert FlatBuffer binary back to JSON
jsonOutput, err := flatc.BinaryToJSON(ctx, schemaID, binaryData)
if err != nil {
    log.Fatalf("Conversion failed: %v", err)
}
fmt.Println(string(jsonOutput))
```

### Querying Schema Metadata

```go
// Get schema description
registry, _ := sds.NewSchemaRegistry()
desc := registry.GetDescription("CDM.fbs")
fmt.Println(desc) // "Conjunction Data Message - Close approach warnings"

// Check if schema exists
if registry.Has("OMM.fbs") {
    content, _ := registry.Get("OMM.fbs")
    fmt.Printf("OMM schema size: %d bytes\n", len(content))
}
```

### Schema-Specific Topics

Each schema has a dedicated PubSub topic for real-time data:

```typescript
import { getTopicName, getSchemaFromTopic } from '@spacedatanetwork/sdn-js';

// Get topic for a schema
const topic = getTopicName('OMM.fbs');
// Returns: /spacedatanetwork/sds/OMM.fbs

// Parse schema from topic
const schema = getSchemaFromTopic('/spacedatanetwork/sds/CDM.fbs');
// Returns: 'CDM.fbs'
```

### Practical Example: Processing OMM Data

```go
package main

import (
    "context"
    "fmt"

    "github.com/spacedatanetwork/sdn-server/internal/sds"
    "github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func main() {
    ctx := context.Background()

    // Initialize validator with WASM support
    flatc, _ := wasm.NewFlatcModule(ctx, "./flatc.wasm")
    validator, _ := sds.NewValidator(flatc)

    // Receive OMM data from network
    ommData := receiveFromNetwork() // Your network code

    // Validate against schema
    if err := validator.Validate(ctx, "OMM.fbs", ommData); err != nil {
        fmt.Printf("Invalid OMM: %v\n", err)
        return
    }

    // Convert to JSON for processing
    jsonData, _ := validator.FlatBufferToJSON(ctx, "OMM.fbs", ommData)

    // Process the data
    processOMM(jsonData)
}
```

## Schema File Structure

Understanding the structure of `.fbs` schema files:

```flatbuffers
// Hash: 4052bd4e7b1e02b4ac22e3e908b4f71af8ff6849f6bde1a8d6f025d49ba8e2b3
// Version: 1.0.5
// -----------------------------------END_HEADER

// Include dependencies
include "../RFM/main.fbs";
include "../TIM/main.fbs";

// Documentation comment
// Orbit Mean Elements Message (OMM)
// CCSDS Reference: 502x0b2c1e2

// Enum definitions
enum ephemerisType : byte {
  SGP,
  SGP4,
  SDP4,
  SGP8,
  SDP8
}

/// Orbit Mean Elements Message
table OMM {
  /// CCSDS OMM Version
  CCSDS_OMM_VERS: double;

  /// Creation Date (ISO 8601 UTC format)
  CREATION_DATE: string;

  /// Satellite Name(s)
  OBJECT_NAME: string;

  /// International Designator (YYYY-NNNAAA)
  OBJECT_ID: string;

  // ... additional fields
}

// Root type declaration
root_type OMM;

// File identifier for format detection
file_identifier "$OMM";
```

Key elements:
- **Header** - Hash and version metadata
- **Includes** - Dependencies on other schemas
- **Enums** - Enumerated types for constrained values
- **Tables** - Main data structures
- **root_type** - The primary table in the file
- **file_identifier** - 4-character identifier for format detection

## Best Practices

### Schema Selection

Choose the appropriate schema for your data:

| Use Case | Recommended Schema |
|----------|-------------------|
| Share satellite position | OMM (mean elements) or OEM (ephemeris) |
| Report close approach | CDM (full details) or CSM (summary) |
| Ground station tracking | TDM |
| Identify your organization | EPM |
| Catalog space objects | CAT |
| Coordinate maneuvers | MPE |

### Data Quality

1. **Complete required fields** - Include all mandatory data
2. **Use correct units** - Follow schema documentation (km, degrees, etc.)
3. **Include timestamps** - Use ISO 8601 UTC format
4. **Version properly** - Set CCSDS_*_VERS fields

### Performance Tips

1. **Keep binary format** - Avoid unnecessary JSON conversion
2. **Batch updates** - Group multiple records when possible
3. **Subscribe selectively** - Only subscribe to schemas you need
4. **Cache schema IDs** - Avoid repeated lookups

## External Resources

- [Space Data Standards](https://spacedatastandards.org) - Official schema specifications
- [FlatBuffers Documentation](https://google.github.io/flatbuffers/) - Serialization format details
- [CCSDS Standards](https://public.ccsds.org/) - Source standards from CCSDS
- [Schema Reference](/reference/schemas) - Quick reference for all schemas
