/**
 * JSON Schema Generator
 * Converts XTCE types to JSON Schema with x-flatbuffer annotations
 */

import type {
  JSONSchema,
  JSONSchemaProperty,
  ConversionOptions,
} from './types.js';
import type { ParsedXTCE, ParameterTypeInfo } from './parser.js';
import { flattenParameterTypes, flattenParameters } from './parser.js';

/**
 * Map XTCE integer size to FlatBuffer type
 */
function integerSizeToFlatBufferType(sizeInBits: number, signed: boolean): string {
  if (signed) {
    if (sizeInBits <= 8) return 'int8';
    if (sizeInBits <= 16) return 'int16';
    if (sizeInBits <= 32) return 'int32';
    return 'int64';
  } else {
    if (sizeInBits <= 8) return 'uint8';
    if (sizeInBits <= 16) return 'uint16';
    if (sizeInBits <= 32) return 'uint32';
    return 'uint64';
  }
}

/**
 * Map XTCE float size to FlatBuffer type
 */
function floatSizeToFlatBufferType(sizeInBits: number): string {
  if (sizeInBits <= 32) return 'float32';
  return 'float64';
}

/**
 * Get JSON Schema integer range based on size
 */
function getIntegerRange(sizeInBits: number, signed: boolean): { min: number; max: number } {
  if (signed) {
    const halfRange = Math.pow(2, sizeInBits - 1);
    return { min: -halfRange, max: halfRange - 1 };
  } else {
    return { min: 0, max: Math.pow(2, sizeInBits) - 1 };
  }
}

/**
 * Convert a single parameter type to JSON Schema property
 */
function parameterTypeToJsonSchemaProperty(
  typeInfo: ParameterTypeInfo,
  fieldId: number
): JSONSchemaProperty {
  const property: JSONSchemaProperty = {
    'x-flatbuffer-field-id': fieldId,
  };

  if (typeInfo.description) {
    property.description = typeInfo.description;
  }

  // Add XTCE-specific annotations
  if (typeInfo.unit) {
    property['x-xtce-unit'] = typeInfo.unit;
  }
  if (typeInfo.unitDescription) {
    property['x-xtce-unit-description'] = typeInfo.unitDescription;
  }
  if (typeInfo.encoding) {
    property['x-xtce-encoding'] = typeInfo.encoding;
  }
  if (typeInfo.encodingSizeInBits) {
    property['x-xtce-encoding-size'] = typeInfo.encodingSizeInBits;
  }
  if (typeInfo.calibrator) {
    property['x-xtce-calibrator'] = typeInfo.calibrator;
  }

  switch (typeInfo.type) {
    case 'integer': {
      property.type = 'integer';
      const signed = typeInfo.signed ?? true;
      const sizeInBits = typeInfo.sizeInBits ?? 32;
      property['x-flatbuffer-type'] = integerSizeToFlatBufferType(sizeInBits, signed);

      // Add range constraints
      const range = getIntegerRange(sizeInBits, signed);
      if (typeInfo.validRange) {
        property.minimum = typeInfo.validRange.minInclusive ?? range.min;
        property.maximum = typeInfo.validRange.maxInclusive ?? range.max;
        if (typeInfo.validRange.minExclusive !== undefined) {
          property.exclusiveMinimum = typeInfo.validRange.minExclusive;
        }
        if (typeInfo.validRange.maxExclusive !== undefined) {
          property.exclusiveMaximum = typeInfo.validRange.maxExclusive;
        }
      } else {
        property.minimum = range.min;
        property.maximum = range.max;
      }
      break;
    }

    case 'float': {
      property.type = 'number';
      const sizeInBits = typeInfo.sizeInBits ?? 32;
      property['x-flatbuffer-type'] = floatSizeToFlatBufferType(sizeInBits);

      // Add range constraints if specified
      if (typeInfo.validRange) {
        if (typeInfo.validRange.minInclusive !== undefined) {
          property.minimum = typeInfo.validRange.minInclusive;
        }
        if (typeInfo.validRange.maxInclusive !== undefined) {
          property.maximum = typeInfo.validRange.maxInclusive;
        }
        if (typeInfo.validRange.minExclusive !== undefined) {
          property.exclusiveMinimum = typeInfo.validRange.minExclusive;
        }
        if (typeInfo.validRange.maxExclusive !== undefined) {
          property.exclusiveMaximum = typeInfo.validRange.maxExclusive;
        }
      }
      break;
    }

    case 'string': {
      property.type = 'string';
      property['x-flatbuffer-type'] = 'string';

      // Add length constraint if known
      if (typeInfo.sizeInBits) {
        // Convert bits to characters (assuming 8 bits per char for UTF-8)
        const maxLength = Math.floor(typeInfo.sizeInBits / 8);
        property.maxLength = maxLength;
      }
      break;
    }

    case 'enumerated': {
      property.type = 'string';
      property['x-flatbuffer-type'] = 'int32'; // Enums stored as int in FlatBuffers

      if (typeInfo.enumerations && typeInfo.enumerations.length > 0) {
        property.enum = typeInfo.enumerations.map(e => e.label);
        // Store value mapping in extension
        const enumMap = typeInfo.enumerations.reduce(
          (acc, e) => {
            acc[e.label] = e.value;
            return acc;
          },
          {} as Record<string, number>
        );
        (property as Record<string, unknown>)['x-xtce-enum-values'] = enumMap;
      }
      break;
    }

    case 'boolean': {
      property.type = 'boolean';
      property['x-flatbuffer-type'] = 'bool';

      if (typeInfo.booleanLabels) {
        (property as Record<string, unknown>)['x-xtce-boolean-labels'] = typeInfo.booleanLabels;
      }
      break;
    }

    case 'time': {
      property.type = 'string';
      property.format = 'date-time';
      property['x-flatbuffer-type'] = typeInfo.encoding === 'float' ? 'float64' : 'int64';

      if (typeInfo.referenceEpoch) {
        (property as Record<string, unknown>)['x-xtce-reference-epoch'] = typeInfo.referenceEpoch;
      }
      break;
    }

    case 'array': {
      property.type = 'array';
      property['x-flatbuffer-type'] = '[' + (typeInfo.arrayTypeRef ?? 'ubyte') + ']';

      // Add dimension info
      if (typeInfo.dimensions && typeInfo.dimensions.length > 0) {
        const dim = typeInfo.dimensions[0];
        if (dim) {
          const size = dim.end - dim.start + 1;
          property.minItems = size;
          property.maxItems = size;
        }
        (property as Record<string, unknown>)['x-xtce-dimensions'] = typeInfo.dimensions;
      }
      break;
    }

    case 'binary': {
      property.type = 'string';
      property.format = 'binary'; // base64 encoded
      property['x-flatbuffer-type'] = '[ubyte]';

      if (typeInfo.sizeInBits) {
        const byteSize = Math.ceil(typeInfo.sizeInBits / 8);
        property.minLength = byteSize;
        property.maxLength = byteSize;
      }
      break;
    }

    default: {
      // Unknown type - treat as opaque binary
      property.type = 'string';
      property.format = 'binary';
      property['x-flatbuffer-type'] = '[ubyte]';
    }
  }

  return property;
}

/**
 * Generate JSON Schema from parsed XTCE
 */
export function generateJsonSchema(
  parsed: ParsedXTCE,
  options: ConversionOptions = {}
): JSONSchema {
  const {
    schemaId,
    includeTelemetry = true,
    includeCommands = true,
    fieldIdOffset = 0,
  } = options;

  const schema: JSONSchema = {
    $schema: 'https://json-schema.org/draft/2019-09/schema',
    title: parsed.name,
    description: parsed.description ?? `XTCE schema converted from ${parsed.name}`,
    type: 'object',
    properties: {},
    definitions: {},
    additionalProperties: false,
  };

  if (schemaId) {
    schema.$id = schemaId;
  }

  // Collect all parameter types
  const allTypes = flattenParameterTypes(parsed);

  // Generate definitions for all types
  let definitionFieldId = fieldIdOffset;
  for (const [name, typeInfo] of allTypes) {
    const sanitizedName = sanitizeName(name);
    schema.definitions![sanitizedName] = parameterTypeToJsonSchemaProperty(
      typeInfo,
      definitionFieldId++
    );
  }

  // Generate properties from parameters
  let fieldId = fieldIdOffset;
  const required: string[] = [];

  if (includeTelemetry) {
    const params = flattenParameters(parsed);
    for (const param of params) {
      const paramName = sanitizeName(param['@_name']);
      const typeRef = param['@_parameterTypeRef'];
      const typeInfo = allTypes.get(typeRef);

      if (typeInfo) {
        schema.properties![paramName] = parameterTypeToJsonSchemaProperty(typeInfo, fieldId++);
        if (param['@_shortDescription']) {
          schema.properties![paramName].description = param['@_shortDescription'];
        }
      } else {
        // Type not found - create reference to definition
        schema.properties![paramName] = {
          $ref: `#/definitions/${sanitizeName(typeRef)}`,
          'x-flatbuffer-field-id': fieldId++,
        } as JSONSchemaProperty;
      }
    }
  }

  if (includeCommands) {
    // Add command arguments as properties
    for (const arg of parsed.commands.arguments) {
      const argName = `cmd_${sanitizeName(arg['@_name'])}`;
      const typeRef = arg['@_argumentTypeRef'];
      const typeInfo = parsed.commands.argumentTypes.get(typeRef) ?? allTypes.get(typeRef);

      if (typeInfo) {
        schema.properties![argName] = parameterTypeToJsonSchemaProperty(typeInfo, fieldId++);
        if (arg['@_shortDescription']) {
          schema.properties![argName].description = arg['@_shortDescription'];
        }
      } else {
        schema.properties![argName] = {
          $ref: `#/definitions/${sanitizeName(typeRef)}`,
          'x-flatbuffer-field-id': fieldId++,
        } as JSONSchemaProperty;
      }
    }
  }

  if (required.length > 0) {
    schema.required = required;
  }

  return schema;
}

/**
 * Sanitize a name for use as JSON Schema property name
 */
function sanitizeName(name: string): string {
  // Replace dots and special chars with underscores
  return name
    .replace(/\./g, '_')
    .replace(/[^a-zA-Z0-9_]/g, '_')
    .replace(/_+/g, '_')
    .replace(/^_|_$/g, '');
}

/**
 * Generate a standalone JSON Schema for telemetry parameters only
 */
export function generateTelemetrySchema(
  parsed: ParsedXTCE,
  options: ConversionOptions = {}
): JSONSchema {
  return generateJsonSchema(parsed, {
    ...options,
    includeTelemetry: true,
    includeCommands: false,
  });
}

/**
 * Generate a standalone JSON Schema for command arguments only
 */
export function generateCommandSchema(
  parsed: ParsedXTCE,
  options: ConversionOptions = {}
): JSONSchema {
  return generateJsonSchema(parsed, {
    ...options,
    includeTelemetry: false,
    includeCommands: true,
  });
}
