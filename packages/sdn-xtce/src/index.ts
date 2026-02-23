/**
 * @sdn/xtce - XTCE to JSON Schema / FlatBuffer Converter
 *
 * Converts XTCE (XML Telemetry/Command Exchange) documents to JSON Schema
 * with x-flatbuffer annotations for FlatBuffer mapping.
 *
 * CCSDS 660.1-G-2 Standard Compliance
 *
 * @example
 * ```typescript
 * import { convertXTCE, convertXTCEToJsonSchema, convertXTCEToFlatBuffer } from '@sdn/xtce';
 *
 * // Full conversion
 * const result = convertXTCE(xtceXml, { namespace: 'MySpacecraft' });
 * console.log(result.jsonSchema);
 * console.log(result.flatBufferSchema);
 *
 * // JSON Schema only
 * const jsonSchema = convertXTCEToJsonSchema(xtceXml);
 *
 * // FlatBuffer schema only
 * const fbsSchema = convertXTCEToFlatBuffer(xtceXml);
 * ```
 */

// Parser exports
export {
  XTCEParser,
  parseXTCE,
  flattenParameterTypes,
  flattenParameters,
  type ParsedXTCE,
  type ParameterTypeInfo,
} from './parser.js';

// JSON Schema generator exports
export {
  generateJsonSchema,
  generateTelemetrySchema,
  generateCommandSchema,
} from './json-schema-generator.js';

// FlatBuffer generator exports
export {
  generateFlatBufferSchema,
  serializeFlatBufferSchema,
  generateFlatBufferSchemaString,
} from './flatbuffer-generator.js';

// Converter exports
export {
  convertXTCE,
  convertXTCEToJsonSchema,
  convertXTCEToFlatBuffer,
  validateAgainstSchema,
  getConversionSummary,
} from './converter.js';

// Type exports
export type {
  // XTCE types
  XTCEDocument,
  XTCESpaceSystem,
  XTCETelemetryMetaData,
  XTCECommandMetaData,
  XTCEParameterTypeSet,
  XTCEParameterSet,
  XTCEParameter,
  XTCEArgument,
  XTCEIntegerParameterType,
  XTCEFloatParameterType,
  XTCEStringParameterType,
  XTCEEnumeratedParameterType,
  XTCEBooleanParameterType,
  XTCEAbsoluteTimeParameterType,
  XTCEArrayParameterType,
  XTCEBinaryParameterType,
  XTCESequenceContainer,
  XTCEMetaCommand,

  // Output types
  JSONSchema,
  JSONSchemaProperty,
  FlatBufferSchema,
  FlatBufferTable,
  FlatBufferField,
  FlatBufferEnum,
  ConversionResult,
  ConversionOptions,
} from './types.js';
