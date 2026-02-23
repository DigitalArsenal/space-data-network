/**
 * XTCE to JSON Schema / FlatBuffer Converter
 * Main converter module that orchestrates the conversion process
 */

import type {
  ConversionResult,
  ConversionOptions,
  JSONSchema,
} from './types.js';
import { parseXTCE, flattenParameters, type ParsedXTCE } from './parser.js';
import { generateJsonSchema } from './json-schema-generator.js';
import {
  generateFlatBufferSchema,
  serializeFlatBufferSchema,
} from './flatbuffer-generator.js';

/**
 * Convert XTCE XML to JSON Schema and FlatBuffer schema
 */
export function convertXTCE(
  xmlContent: string,
  options: ConversionOptions = {}
): ConversionResult {
  const warnings: string[] = [];

  // Parse XTCE XML
  let parsed: ParsedXTCE;
  try {
    parsed = parseXTCE(xmlContent);
  } catch (error) {
    throw new Error(`Failed to parse XTCE XML: ${error instanceof Error ? error.message : String(error)}`);
  }

  // Generate JSON Schema
  const jsonSchema = generateJsonSchema(parsed, options);

  // Generate FlatBuffer Schema
  const flatBufferSchema = generateFlatBufferSchema(parsed, options);

  // Collect telemetry parameters
  const telemetryParameters = flattenParameters(parsed);

  // Collect command arguments
  const commandArguments = parsed.commands.arguments;

  // Check for potential issues
  if (telemetryParameters.length === 0 && commandArguments.length === 0) {
    warnings.push('No parameters or commands found in XTCE document');
  }

  // Check for unresolved type references
  const allTypeNames = new Set<string>();
  for (const [name] of parsed.telemetry.parameterTypes) {
    allTypeNames.add(name);
  }
  for (const [name] of parsed.commands.argumentTypes) {
    allTypeNames.add(name);
  }

  for (const param of telemetryParameters) {
    if (!allTypeNames.has(param['@_parameterTypeRef'])) {
      warnings.push(`Unresolved type reference: ${param['@_parameterTypeRef']} for parameter ${param['@_name']}`);
    }
  }

  for (const arg of commandArguments) {
    if (!allTypeNames.has(arg['@_argumentTypeRef'])) {
      warnings.push(`Unresolved type reference: ${arg['@_argumentTypeRef']} for argument ${arg['@_name']}`);
    }
  }

  return {
    jsonSchema,
    flatBufferSchema,
    telemetryParameters,
    commandArguments,
    warnings,
  };
}

/**
 * Convert XTCE to JSON Schema string
 */
export function convertXTCEToJsonSchema(
  xmlContent: string,
  options: ConversionOptions = {}
): string {
  const result = convertXTCE(xmlContent, options);
  return JSON.stringify(result.jsonSchema, null, 2);
}

/**
 * Convert XTCE to FlatBuffer schema string
 */
export function convertXTCEToFlatBuffer(
  xmlContent: string,
  options: ConversionOptions = {}
): string {
  const result = convertXTCE(xmlContent, options);
  return serializeFlatBufferSchema(result.flatBufferSchema);
}

/**
 * Validate JSON Schema against an object
 * Basic validation - for production use, consider using ajv
 */
export function validateAgainstSchema(
  schema: JSONSchema,
  data: Record<string, unknown>
): { valid: boolean; errors: string[] } {
  const errors: string[] = [];

  // Check required fields
  if (schema.required) {
    for (const field of schema.required) {
      if (!(field in data)) {
        errors.push(`Missing required field: ${field}`);
      }
    }
  }

  // Check property types
  if (schema.properties) {
    for (const [name, propSchema] of Object.entries(schema.properties)) {
      if (name in data) {
        const value = data[name];
        const propType = propSchema.type;

        if (propType === 'integer' || propType === 'number') {
          if (typeof value !== 'number') {
            errors.push(`Field ${name} should be a number, got ${typeof value}`);
          } else {
            if (propSchema.minimum !== undefined && value < propSchema.minimum) {
              errors.push(`Field ${name} is below minimum ${propSchema.minimum}`);
            }
            if (propSchema.maximum !== undefined && value > propSchema.maximum) {
              errors.push(`Field ${name} is above maximum ${propSchema.maximum}`);
            }
          }
        } else if (propType === 'string') {
          if (typeof value !== 'string') {
            errors.push(`Field ${name} should be a string, got ${typeof value}`);
          } else {
            if (propSchema.maxLength !== undefined && value.length > propSchema.maxLength) {
              errors.push(`Field ${name} exceeds max length ${propSchema.maxLength}`);
            }
            if (propSchema.enum && !propSchema.enum.includes(value)) {
              errors.push(`Field ${name} must be one of: ${propSchema.enum.join(', ')}`);
            }
          }
        } else if (propType === 'boolean') {
          if (typeof value !== 'boolean') {
            errors.push(`Field ${name} should be a boolean, got ${typeof value}`);
          }
        } else if (propType === 'array') {
          if (!Array.isArray(value)) {
            errors.push(`Field ${name} should be an array, got ${typeof value}`);
          }
        }
      }
    }
  }

  // Check additionalProperties
  if (schema.additionalProperties === false && schema.properties) {
    const allowedFields = new Set(Object.keys(schema.properties));
    for (const key of Object.keys(data)) {
      if (!allowedFields.has(key)) {
        errors.push(`Unknown field: ${key}`);
      }
    }
  }

  return {
    valid: errors.length === 0,
    errors,
  };
}

/**
 * Get summary statistics from conversion result
 */
export function getConversionSummary(result: ConversionResult): {
  telemetryCount: number;
  commandCount: number;
  propertyCount: number;
  enumCount: number;
  warningCount: number;
} {
  return {
    telemetryCount: result.telemetryParameters.length,
    commandCount: result.commandArguments.length,
    propertyCount: Object.keys(result.jsonSchema.properties ?? {}).length,
    enumCount: result.flatBufferSchema.enums.length,
    warningCount: result.warnings.length,
  };
}
