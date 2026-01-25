/**
 * FlatBuffer Schema Generator
 * Converts XTCE types to FlatBuffer schema (.fbs)
 */

import type {
  FlatBufferSchema,
  FlatBufferTable,
  FlatBufferField,
  FlatBufferEnum,
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
    if (sizeInBits <= 8) return 'ubyte';
    if (sizeInBits <= 16) return 'ushort';
    if (sizeInBits <= 32) return 'uint';
    return 'ulong';
  }
}

/**
 * Map XTCE float size to FlatBuffer type
 */
function floatSizeToFlatBufferType(sizeInBits: number): string {
  if (sizeInBits <= 32) return 'float';
  return 'double';
}

/**
 * Convert parameter type to FlatBuffer field type
 */
function parameterTypeToFlatBufferType(typeInfo: ParameterTypeInfo): string {
  switch (typeInfo.type) {
    case 'integer':
      return integerSizeToFlatBufferType(
        typeInfo.sizeInBits ?? 32,
        typeInfo.signed ?? true
      );

    case 'float':
      return floatSizeToFlatBufferType(typeInfo.sizeInBits ?? 32);

    case 'string':
      return 'string';

    case 'enumerated':
      // Return enum name - will be defined separately
      return sanitizeName(typeInfo.name);

    case 'boolean':
      return 'bool';

    case 'time':
      // Store as int64 (Unix timestamp) or double
      return typeInfo.encoding === 'float' ? 'double' : 'long';

    case 'array':
      // Return array of the referenced type
      return `[${typeInfo.arrayTypeRef ?? 'ubyte'}]`;

    case 'binary':
      return '[ubyte]';

    default:
      return '[ubyte]';
  }
}

/**
 * Sanitize a name for FlatBuffer identifier
 */
function sanitizeName(name: string): string {
  return name
    .replace(/\./g, '_')
    .replace(/[^a-zA-Z0-9_]/g, '_')
    .replace(/_+/g, '_')
    .replace(/^_|_$/g, '')
    .replace(/^(\d)/, '_$1'); // Can't start with digit
}

/**
 * Generate FlatBuffer enums from enumerated types
 */
function generateEnums(types: Map<string, ParameterTypeInfo>): FlatBufferEnum[] {
  const enums: FlatBufferEnum[] = [];

  for (const [, typeInfo] of types) {
    if (typeInfo.type === 'enumerated' && typeInfo.enumerations) {
      const enumDef: FlatBufferEnum = {
        name: sanitizeName(typeInfo.name),
        type: 'int',
        values: typeInfo.enumerations.map(e => ({
          name: sanitizeName(e.label),
          value: e.value,
        })),
        comment: typeInfo.description,
      };
      enums.push(enumDef);
    }
  }

  return enums;
}

/**
 * Generate FlatBuffer schema from parsed XTCE
 */
export function generateFlatBufferSchema(
  parsed: ParsedXTCE,
  options: ConversionOptions = {}
): FlatBufferSchema {
  const {
    namespace,
    includeTelemetry = true,
    includeCommands = true,
    generateEnums: genEnums = true,
    fieldIdOffset = 0,
  } = options;

  const schema: FlatBufferSchema = {
    namespace,
    fileIdentifier: `$${parsed.name.substring(0, 3).toUpperCase()}`,
    rootType: sanitizeName(parsed.name),
    enums: [],
    tables: [],
  };

  // Collect all parameter types
  const allTypes = flattenParameterTypes(parsed);

  // Generate enums from enumerated types
  if (genEnums) {
    schema.enums = generateEnums(allTypes);
  }

  // Generate main table
  const mainTable: FlatBufferTable = {
    name: sanitizeName(parsed.name),
    fields: [],
    comment: parsed.description,
  };

  let fieldId = fieldIdOffset;

  // Add telemetry parameters as fields
  if (includeTelemetry) {
    const params = flattenParameters(parsed);
    for (const param of params) {
      const typeRef = param['@_parameterTypeRef'];
      const typeInfo = allTypes.get(typeRef);

      if (typeInfo) {
        const field: FlatBufferField = {
          name: sanitizeName(param['@_name']),
          type: parameterTypeToFlatBufferType(typeInfo),
          id: fieldId++,
          comment: param['@_shortDescription'] ?? typeInfo.description,
        };

        mainTable.fields.push(field);
      }
    }
  }

  // Add command arguments as fields
  if (includeCommands) {
    for (const arg of parsed.commands.arguments) {
      const typeRef = arg['@_argumentTypeRef'];
      const typeInfo = parsed.commands.argumentTypes.get(typeRef) ?? allTypes.get(typeRef);

      if (typeInfo) {
        const field: FlatBufferField = {
          name: `cmd_${sanitizeName(arg['@_name'])}`,
          type: parameterTypeToFlatBufferType(typeInfo),
          id: fieldId++,
          comment: arg['@_shortDescription'] ?? typeInfo.description,
        };

        mainTable.fields.push(field);
      }
    }
  }

  schema.tables.push(mainTable);

  return schema;
}

/**
 * Serialize FlatBuffer schema to .fbs format
 */
export function serializeFlatBufferSchema(schema: FlatBufferSchema): string {
  const lines: string[] = [];

  // Header comment
  lines.push('// Auto-generated FlatBuffer schema from XTCE');
  lines.push(`// Generated: ${new Date().toISOString()}`);
  lines.push('');

  // Namespace
  if (schema.namespace) {
    lines.push(`namespace ${schema.namespace};`);
    lines.push('');
  }

  // File identifier
  if (schema.fileIdentifier) {
    lines.push(`file_identifier "${schema.fileIdentifier}";`);
    lines.push('');
  }

  // Enums
  for (const enumDef of schema.enums) {
    if (enumDef.comment) {
      lines.push(`/// ${enumDef.comment}`);
    }
    lines.push(`enum ${enumDef.name} : ${enumDef.type} {`);
    for (const value of enumDef.values) {
      lines.push(`  ${value.name} = ${value.value},`);
    }
    lines.push('}');
    lines.push('');
  }

  // Tables
  for (const table of schema.tables) {
    if (table.comment) {
      lines.push(`/// ${table.comment}`);
    }
    lines.push(`table ${table.name} {`);

    for (const field of table.fields) {
      let fieldLine = '  ';

      if (field.comment) {
        lines.push(`  /// ${field.comment}`);
      }

      fieldLine += `${field.name}:${field.type}`;

      // Add field id attribute
      fieldLine += ` (id: ${field.id}`;

      // Add deprecated if needed
      if (field.deprecated) {
        fieldLine += ', deprecated';
      }

      fieldLine += ');';
      lines.push(fieldLine);
    }

    lines.push('}');
    lines.push('');
  }

  // Root type
  if (schema.rootType) {
    lines.push(`root_type ${schema.rootType};`);
  }

  return lines.join('\n');
}

/**
 * Generate FlatBuffer schema string directly
 */
export function generateFlatBufferSchemaString(
  parsed: ParsedXTCE,
  options: ConversionOptions = {}
): string {
  const schema = generateFlatBufferSchema(parsed, options);
  return serializeFlatBufferSchema(schema);
}
