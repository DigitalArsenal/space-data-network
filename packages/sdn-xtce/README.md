# @sdn/xtce

XTCE (XML Telemetry/Command Exchange) to JSON Schema converter with FlatBuffer annotations.

Converts XTCE documents (CCSDS 660.1-G-2 standard) to JSON Schema with `x-flatbuffer-*` annotations for FlatBuffer mapping.

## Features

- **XTCE Parser**: Full support for CCSDS 660.1-G-2 standard
  - Telemetry parameter definitions
  - Command definitions
  - Parameter types (Integer, Float, String, Enumerated, Boolean, Time, Array, Binary)
  - Inheritance and reference resolution
  - Nested SpaceSystem support

- **JSON Schema Generator**
  - Converts XTCE types to JSON Schema types
  - `x-flatbuffer-type` annotations for FlatBuffer mapping
  - `x-flatbuffer-field-id` for stable serialization
  - `x-xtce-*` annotations preserving XTCE metadata (units, encoding, calibrators)

- **FlatBuffer Schema Generator**
  - Generates `.fbs` files compatible with `flatc` compiler
  - Enum generation from enumerated types
  - Proper field ID assignment

## Installation

```bash
npm install @sdn/xtce
```

## CLI Usage

```bash
# Convert XTCE to both JSON Schema and FlatBuffer schema
sdn-xtce convert --input spacecraft.xml --output-schema spacecraft.schema.json --output-fbs spacecraft.fbs

# Convert with namespace
sdn-xtce convert -i spacecraft.xml -s spacecraft.schema.json -f spacecraft.fbs -n MySpacecraft

# Show information about an XTCE file
sdn-xtce info -i spacecraft.xml -v

# Validate JSON data against generated schema
sdn-xtce validate -s spacecraft.schema.json -d telemetry.json
```

### CLI Options

```
sdn-xtce convert [options]

Options:
  -i, --input <file>           Input XTCE XML file (required)
  -s, --output-schema <file>   Output JSON Schema file
  -f, --output-fbs <file>      Output FlatBuffer schema file
  -n, --namespace <namespace>  FlatBuffer namespace
  --no-telemetry               Exclude telemetry parameters
  --no-commands                Exclude command definitions
  --no-enums                   Do not generate FlatBuffer enums
  --field-id-offset <number>   Starting field ID offset (default: 0)
  --schema-id <uri>            JSON Schema $id URI
  -q, --quiet                  Suppress output messages
  -v, --verbose                Show detailed output
```

## API Usage

```typescript
import {
  parseXTCE,
  convertXTCE,
  convertXTCEToJsonSchema,
  convertXTCEToFlatBuffer,
} from '@sdn/xtce';

// Parse XTCE XML
const parsed = parseXTCE(xtceXml);
console.log(`Spacecraft: ${parsed.name}`);
console.log(`Parameters: ${parsed.telemetry.parameters.length}`);

// Full conversion
const result = convertXTCE(xtceXml, {
  namespace: 'MySpacecraft',
  schemaId: 'https://example.com/schemas/spacecraft.json',
});
console.log(result.jsonSchema);
console.log(result.flatBufferSchema);
console.log(`Warnings: ${result.warnings}`);

// JSON Schema only
const jsonSchema = convertXTCEToJsonSchema(xtceXml);

// FlatBuffer schema only
const fbsSchema = convertXTCEToFlatBuffer(xtceXml);
```

## Output Examples

### Input XTCE

```xml
<xtce:SpaceSystem name="ExampleSat">
  <xtce:TelemetryMetaData>
    <xtce:ParameterTypeSet>
      <xtce:IntegerParameterType name="Temperature" signed="true" sizeInBits="16">
        <xtce:UnitSet>
          <xtce:Unit description="Temperature">degC</xtce:Unit>
        </xtce:UnitSet>
        <xtce:IntegerDataEncoding sizeInBits="16" encoding="twosComplement"/>
        <xtce:ValidRange minInclusive="-40" maxInclusive="85"/>
      </xtce:IntegerParameterType>
    </xtce:ParameterTypeSet>
    <xtce:ParameterSet>
      <xtce:Parameter name="TEMP_SENSOR" parameterTypeRef="Temperature"/>
    </xtce:ParameterSet>
  </xtce:TelemetryMetaData>
</xtce:SpaceSystem>
```

### Output JSON Schema

```json
{
  "$schema": "https://json-schema.org/draft/2019-09/schema",
  "title": "ExampleSat",
  "type": "object",
  "properties": {
    "TEMP_SENSOR": {
      "type": "integer",
      "x-flatbuffer-type": "int16",
      "x-flatbuffer-field-id": 0,
      "x-xtce-unit": "degC",
      "x-xtce-encoding": "twosComplement",
      "minimum": -40,
      "maximum": 85
    }
  }
}
```

### Output FlatBuffer Schema

```flatbuffers
// Auto-generated FlatBuffer schema from XTCE
namespace ExampleSat;

table ExampleSat {
  /// Temperature sensor reading
  TEMP_SENSOR:int16 (id: 0);
}

root_type ExampleSat;
```

## x-flatbuffer Annotations

| Annotation | Description |
|------------|-------------|
| `x-flatbuffer-type` | FlatBuffer scalar type (`int8`, `int16`, `int32`, `int64`, `uint8`, `uint16`, `uint32`, `uint64`, `float32`, `float64`, `bool`, `string`, `[ubyte]`) |
| `x-flatbuffer-field-id` | Stable field ID for binary compatibility |
| `x-flatbuffer-deprecated` | Mark field as deprecated |

## x-xtce Annotations

| Annotation | Description |
|------------|-------------|
| `x-xtce-unit` | Unit symbol from XTCE UnitSet |
| `x-xtce-unit-description` | Unit description |
| `x-xtce-encoding` | Wire encoding (unsigned, twosComplement, IEEE754_1985, etc.) |
| `x-xtce-encoding-size` | Size in bits on wire |
| `x-xtce-calibrator` | Calibration information (polynomial, spline, etc.) |
| `x-xtce-enum-values` | Mapping of enum labels to numeric values |
| `x-xtce-boolean-labels` | Labels for boolean true/false values |
| `x-xtce-reference-epoch` | Reference epoch for time types |
| `x-xtce-dimensions` | Array dimension information |

## Type Mapping

| XTCE Type | JSON Schema Type | FlatBuffer Type |
|-----------|------------------|-----------------|
| IntegerParameterType (signed, <=8 bits) | integer | int8 |
| IntegerParameterType (signed, <=16 bits) | integer | int16 |
| IntegerParameterType (signed, <=32 bits) | integer | int32 |
| IntegerParameterType (signed, <=64 bits) | integer | int64 |
| IntegerParameterType (unsigned, <=8 bits) | integer | uint8 |
| IntegerParameterType (unsigned, <=16 bits) | integer | uint16 |
| IntegerParameterType (unsigned, <=32 bits) | integer | uint32 |
| IntegerParameterType (unsigned, <=64 bits) | integer | uint64 |
| FloatParameterType (<=32 bits) | number | float |
| FloatParameterType (>32 bits) | number | double |
| StringParameterType | string | string |
| EnumeratedParameterType | string (enum) | enum |
| BooleanParameterType | boolean | bool |
| AbsoluteTimeParameterType | string (date-time) | int64/double |
| ArrayParameterType | array | [type] |
| BinaryParameterType | string (binary) | [ubyte] |

## Integration with SDN

The generated schemas are designed to work with the Space Data Network:

1. **JSON Schema**: Use for validation in sdn-js or any JSON Schema validator
2. **FlatBuffer Schema**: Compile with `flatc` for binary serialization
3. **x-flatbuffer annotations**: Used by SDN's flatsql storage for querying

## License

MIT
