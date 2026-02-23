/**
 * XTCE Parser Module
 * Parses XTCE XML format (CCSDS 660.1-G-2 standard)
 */

import { XMLParser } from 'fast-xml-parser';
import type {
  XTCEDocument,
  XTCESpaceSystem,
  XTCETelemetryMetaData,
  XTCECommandMetaData,
  XTCEParameterTypeSet,
  XTCEParameterSet,
  XTCEArgumentTypeSet,
  XTCEMetaCommandSet,
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
  XTCEMetaCommand,
} from './types.js';

/**
 * Parsed XTCE result containing all extracted information
 */
export interface ParsedXTCE {
  name: string;
  description?: string;
  telemetry: {
    parameterTypes: Map<string, ParameterTypeInfo>;
    parameters: XTCEParameter[];
  };
  commands: {
    argumentTypes: Map<string, ParameterTypeInfo>;
    arguments: XTCEArgument[];
    commands: XTCEMetaCommand[];
  };
  subSystems: ParsedXTCE[];
}

/**
 * Unified parameter type information
 */
export interface ParameterTypeInfo {
  name: string;
  type: 'integer' | 'float' | 'string' | 'enumerated' | 'boolean' | 'time' | 'array' | 'binary';
  description?: string;
  signed?: boolean;
  sizeInBits?: number;
  encoding?: string;
  encodingSizeInBits?: number;
  unit?: string;
  unitDescription?: string;
  validRange?: {
    minInclusive?: number;
    maxInclusive?: number;
    minExclusive?: number;
    maxExclusive?: number;
  };
  enumerations?: Array<{ value: number; label: string; description?: string }>;
  booleanLabels?: { zero: string; one: string };
  arrayTypeRef?: string;
  dimensions?: Array<{ start: number; end: number }>;
  calibrator?: unknown;
  referenceEpoch?: string;
  raw: unknown; // Original XTCE element
}

/**
 * XTCE Parser class
 */
export class XTCEParser {
  private xmlParser: XMLParser;

  constructor() {
    this.xmlParser = new XMLParser({
      ignoreAttributes: false,
      attributeNamePrefix: '@_',
      textNodeName: '#text',
      parseAttributeValue: false,
      trimValues: true,
      removeNSPrefix: true, // Remove namespace prefixes like 'xtce:'
      isArray: (name) => {
        // Elements that can appear multiple times (without namespace prefix)
        const arrayElements = [
          'Parameter',
          'Argument',
          'IntegerParameterType',
          'FloatParameterType',
          'StringParameterType',
          'EnumeratedParameterType',
          'BooleanParameterType',
          'AbsoluteTimeParameterType',
          'ArrayParameterType',
          'BinaryParameterType',
          'IntegerArgumentType',
          'FloatArgumentType',
          'StringArgumentType',
          'EnumeratedArgumentType',
          'BooleanArgumentType',
          'BinaryArgumentType',
          'Enumeration',
          'Unit',
          'Term',
          'SplinePoint',
          'SpaceSystem',
          'SequenceContainer',
          'MetaCommand',
          'ParameterRefEntry',
          'ArgumentRefEntry',
          'Comparison',
          'Alias',
          'AncillaryData',
          'Dimension',
          'ArgumentAssignment',
        ];
        // Check both with and without namespace prefix
        const baseName = name.includes(':') ? name.split(':')[1] : name;
        return arrayElements.includes(baseName ?? name);
      },
    });
  }

  /**
   * Parse XTCE XML content
   */
  parse(xmlContent: string): ParsedXTCE {
    const doc = this.xmlParser.parse(xmlContent) as XTCEDocument;

    // Handle both namespaced and non-namespaced root elements
    let rootSystem = doc.SpaceSystem ?? doc['xtce:SpaceSystem'];

    if (!rootSystem) {
      throw new Error('Invalid XTCE document: missing SpaceSystem root element');
    }

    // The parser may return an array for SpaceSystem (due to isArray config)
    // Get the first element if it's an array
    if (Array.isArray(rootSystem)) {
      rootSystem = rootSystem[0];
    }

    if (!rootSystem) {
      throw new Error('Invalid XTCE document: empty SpaceSystem element');
    }

    return this.parseSpaceSystem(rootSystem);
  }

  /**
   * Parse a SpaceSystem element
   */
  private parseSpaceSystem(system: XTCESpaceSystem): ParsedXTCE {
    const result: ParsedXTCE = {
      name: system['@_name'] ?? 'Unknown',
      description: system['@_shortDescription'],
      telemetry: {
        parameterTypes: new Map(),
        parameters: [],
      },
      commands: {
        argumentTypes: new Map(),
        arguments: [],
        commands: [],
      },
      subSystems: [],
    };

    // Parse telemetry metadata
    if (system.TelemetryMetaData) {
      this.parseTelemetryMetaData(system.TelemetryMetaData, result);
    }

    // Parse command metadata
    if (system.CommandMetaData) {
      this.parseCommandMetaData(system.CommandMetaData, result);
    }

    // Parse nested SpaceSystems
    if (system.SpaceSystem) {
      const subSystems = Array.isArray(system.SpaceSystem)
        ? system.SpaceSystem
        : [system.SpaceSystem];

      for (const subSystem of subSystems) {
        result.subSystems.push(this.parseSpaceSystem(subSystem));
      }
    }

    return result;
  }

  /**
   * Parse TelemetryMetaData section
   */
  private parseTelemetryMetaData(
    telemetry: XTCETelemetryMetaData,
    result: ParsedXTCE
  ): void {
    // Parse parameter types
    if (telemetry.ParameterTypeSet) {
      this.parseParameterTypeSet(telemetry.ParameterTypeSet, result.telemetry.parameterTypes);
    }

    // Parse parameters
    if (telemetry.ParameterSet) {
      result.telemetry.parameters = this.parseParameterSet(telemetry.ParameterSet);
    }
  }

  /**
   * Parse CommandMetaData section
   */
  private parseCommandMetaData(
    commands: XTCECommandMetaData,
    result: ParsedXTCE
  ): void {
    // Parse argument types
    if (commands.ArgumentTypeSet) {
      this.parseArgumentTypeSet(commands.ArgumentTypeSet, result.commands.argumentTypes);
    }

    // Also check ParameterTypeSet in commands (used for command arguments)
    if (commands.ParameterTypeSet) {
      this.parseParameterTypeSet(commands.ParameterTypeSet, result.commands.argumentTypes);
    }

    // Parse meta commands
    if (commands.MetaCommandSet) {
      result.commands.commands = this.parseMetaCommandSet(commands.MetaCommandSet);

      // Extract arguments from commands
      for (const cmd of result.commands.commands) {
        if (cmd.ArgumentList?.Argument) {
          const args = Array.isArray(cmd.ArgumentList.Argument)
            ? cmd.ArgumentList.Argument
            : [cmd.ArgumentList.Argument];
          result.commands.arguments.push(...args);
        }
      }
    }
  }

  /**
   * Parse ParameterTypeSet
   */
  private parseParameterTypeSet(
    typeSet: XTCEParameterTypeSet,
    targetMap: Map<string, ParameterTypeInfo>
  ): void {
    // Integer types
    if (typeSet.IntegerParameterType) {
      const types = this.ensureArray(typeSet.IntegerParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseIntegerType(t));
      }
    }

    // Float types
    if (typeSet.FloatParameterType) {
      const types = this.ensureArray(typeSet.FloatParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseFloatType(t));
      }
    }

    // String types
    if (typeSet.StringParameterType) {
      const types = this.ensureArray(typeSet.StringParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseStringType(t));
      }
    }

    // Enumerated types
    if (typeSet.EnumeratedParameterType) {
      const types = this.ensureArray(typeSet.EnumeratedParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseEnumeratedType(t));
      }
    }

    // Boolean types
    if (typeSet.BooleanParameterType) {
      const types = this.ensureArray(typeSet.BooleanParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseBooleanType(t));
      }
    }

    // Time types
    if (typeSet.AbsoluteTimeParameterType) {
      const types = this.ensureArray(typeSet.AbsoluteTimeParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseTimeType(t));
      }
    }

    // Array types
    if (typeSet.ArrayParameterType) {
      const types = this.ensureArray(typeSet.ArrayParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseArrayType(t));
      }
    }

    // Binary types
    if (typeSet.BinaryParameterType) {
      const types = this.ensureArray(typeSet.BinaryParameterType);
      for (const t of types) {
        targetMap.set(t['@_name'], this.parseBinaryType(t));
      }
    }
  }

  /**
   * Parse ArgumentTypeSet (same structure as ParameterTypeSet)
   */
  private parseArgumentTypeSet(
    typeSet: XTCEArgumentTypeSet,
    targetMap: Map<string, ParameterTypeInfo>
  ): void {
    // Use the same parsing logic - the types have the same structure
    this.parseParameterTypeSet(typeSet as XTCEParameterTypeSet, targetMap);
  }

  /**
   * Parse ParameterSet
   */
  private parseParameterSet(paramSet: XTCEParameterSet): XTCEParameter[] {
    if (!paramSet.Parameter) {
      return [];
    }
    return this.ensureArray(paramSet.Parameter);
  }

  /**
   * Parse MetaCommandSet
   */
  private parseMetaCommandSet(cmdSet: XTCEMetaCommandSet): XTCEMetaCommand[] {
    if (!cmdSet.MetaCommand) {
      return [];
    }
    return this.ensureArray(cmdSet.MetaCommand);
  }

  /**
   * Parse IntegerParameterType
   */
  private parseIntegerType(type: XTCEIntegerParameterType): ParameterTypeInfo {
    const signed = type['@_signed'] !== 'false';
    const sizeInBits = this.parseNumber(type['@_sizeInBits'], 32);
    const encoding = type.IntegerDataEncoding;

    return {
      name: type['@_name'],
      type: 'integer',
      description: type['@_shortDescription'],
      signed,
      sizeInBits,
      encoding: encoding?.['@_encoding'] ?? (signed ? 'twosComplement' : 'unsigned'),
      encodingSizeInBits: this.parseNumber(encoding?.['@_sizeInBits'], sizeInBits),
      unit: this.extractUnit(type.UnitSet),
      unitDescription: this.extractUnitDescription(type.UnitSet),
      validRange: this.parseValidRange(type.ValidRange),
      calibrator: encoding?.DefaultCalibrator,
      raw: type,
    };
  }

  /**
   * Parse FloatParameterType
   */
  private parseFloatType(type: XTCEFloatParameterType): ParameterTypeInfo {
    const sizeInBits = this.parseNumber(type['@_sizeInBits'], 32);
    const floatEncoding = type.FloatDataEncoding;
    const intEncoding = type.IntegerDataEncoding;

    return {
      name: type['@_name'],
      type: 'float',
      description: type['@_shortDescription'],
      sizeInBits,
      encoding: floatEncoding?.['@_encoding'] ?? intEncoding?.['@_encoding'] ?? 'IEEE754_1985',
      encodingSizeInBits: this.parseNumber(
        floatEncoding?.['@_sizeInBits'] ?? intEncoding?.['@_sizeInBits'],
        sizeInBits
      ),
      unit: this.extractUnit(type.UnitSet),
      unitDescription: this.extractUnitDescription(type.UnitSet),
      validRange: this.parseValidRange(type.ValidRange),
      calibrator: type.DefaultCalibrator ?? intEncoding?.DefaultCalibrator,
      raw: type,
    };
  }

  /**
   * Parse StringParameterType
   */
  private parseStringType(type: XTCEStringParameterType): ParameterTypeInfo {
    const encoding = type.StringDataEncoding;
    let sizeInBits: number | undefined;

    if (encoding?.SizeInBits?.Fixed?.FixedValue) {
      sizeInBits = this.parseNumber(encoding.SizeInBits.Fixed.FixedValue);
    }

    return {
      name: type['@_name'],
      type: 'string',
      description: type['@_shortDescription'],
      sizeInBits,
      encoding: encoding?.['@_encoding'] ?? 'UTF-8',
      raw: type,
    };
  }

  /**
   * Parse EnumeratedParameterType
   */
  private parseEnumeratedType(type: XTCEEnumeratedParameterType): ParameterTypeInfo {
    const encoding = type.IntegerDataEncoding;
    const enumerations: ParameterTypeInfo['enumerations'] = [];

    if (type.EnumerationList?.Enumeration) {
      const enumList = this.ensureArray(type.EnumerationList.Enumeration);
      for (const e of enumList) {
        enumerations.push({
          value: this.parseNumber(e['@_value'], 0),
          label: e['@_label'],
          description: e['@_shortDescription'],
        });
      }
    }

    return {
      name: type['@_name'],
      type: 'enumerated',
      description: type['@_shortDescription'],
      encodingSizeInBits: this.parseNumber(encoding?.['@_sizeInBits'], 8),
      encoding: encoding?.['@_encoding'] ?? 'unsigned',
      enumerations,
      raw: type,
    };
  }

  /**
   * Parse BooleanParameterType
   */
  private parseBooleanType(type: XTCEBooleanParameterType): ParameterTypeInfo {
    const encoding = type.IntegerDataEncoding;

    return {
      name: type['@_name'],
      type: 'boolean',
      description: type['@_shortDescription'],
      encodingSizeInBits: this.parseNumber(encoding?.['@_sizeInBits'], 1),
      booleanLabels: {
        zero: type['@_zeroStringValue'] ?? 'false',
        one: type['@_oneStringValue'] ?? 'true',
      },
      raw: type,
    };
  }

  /**
   * Parse AbsoluteTimeParameterType
   */
  private parseTimeType(type: XTCEAbsoluteTimeParameterType): ParameterTypeInfo {
    const encoding = type.Encoding;
    const intEnc = encoding?.IntegerDataEncoding;
    const floatEnc = encoding?.FloatDataEncoding;

    return {
      name: type['@_name'],
      type: 'time',
      description: type['@_shortDescription'],
      encodingSizeInBits: this.parseNumber(
        intEnc?.['@_sizeInBits'] ?? floatEnc?.['@_sizeInBits'],
        64
      ),
      encoding: intEnc ? 'integer' : 'float',
      referenceEpoch: type.ReferenceTime?.Epoch,
      raw: type,
    };
  }

  /**
   * Parse ArrayParameterType
   */
  private parseArrayType(type: XTCEArrayParameterType): ParameterTypeInfo {
    const dimensions: ParameterTypeInfo['dimensions'] = [];

    if (type.DimensionList?.Dimension) {
      const dimList = this.ensureArray(type.DimensionList.Dimension);
      for (const d of dimList) {
        dimensions.push({
          start: this.parseNumber(d.StartingIndex?.FixedValue, 0),
          end: this.parseNumber(d.EndingIndex?.FixedValue, 0),
        });
      }
    }

    return {
      name: type['@_name'],
      type: 'array',
      description: type['@_shortDescription'],
      arrayTypeRef: type['@_arrayTypeRef'],
      dimensions,
      raw: type,
    };
  }

  /**
   * Parse BinaryParameterType
   */
  private parseBinaryType(type: XTCEBinaryParameterType): ParameterTypeInfo {
    const encoding = type.BinaryDataEncoding;
    let sizeInBits: number | undefined;

    if (encoding?.SizeInBits?.Fixed?.FixedValue) {
      sizeInBits = this.parseNumber(encoding.SizeInBits.Fixed.FixedValue);
    }

    return {
      name: type['@_name'],
      type: 'binary',
      description: type['@_shortDescription'],
      sizeInBits,
      raw: type,
    };
  }

  /**
   * Parse ValidRange
   */
  private parseValidRange(range?: { '@_minInclusive'?: string; '@_maxInclusive'?: string; '@_minExclusive'?: string; '@_maxExclusive'?: string }): ParameterTypeInfo['validRange'] | undefined {
    if (!range) return undefined;

    const result: ParameterTypeInfo['validRange'] = {};

    if (range['@_minInclusive'] !== undefined) {
      result.minInclusive = this.parseNumber(range['@_minInclusive']);
    }
    if (range['@_maxInclusive'] !== undefined) {
      result.maxInclusive = this.parseNumber(range['@_maxInclusive']);
    }
    if (range['@_minExclusive'] !== undefined) {
      result.minExclusive = this.parseNumber(range['@_minExclusive']);
    }
    if (range['@_maxExclusive'] !== undefined) {
      result.maxExclusive = this.parseNumber(range['@_maxExclusive']);
    }

    return Object.keys(result).length > 0 ? result : undefined;
  }

  /**
   * Extract unit from UnitSet
   */
  private extractUnit(unitSet?: { Unit?: unknown }): string | undefined {
    if (!unitSet?.Unit) return undefined;

    const units = this.ensureArray(unitSet.Unit);
    if (units.length === 0) return undefined;

    const unit = units[0] as { '#text'?: string };
    return unit['#text'];
  }

  /**
   * Extract unit description from UnitSet
   */
  private extractUnitDescription(unitSet?: { Unit?: unknown }): string | undefined {
    if (!unitSet?.Unit) return undefined;

    const units = this.ensureArray(unitSet.Unit);
    if (units.length === 0) return undefined;

    const unit = units[0] as { '@_description'?: string };
    return unit['@_description'];
  }

  /**
   * Parse a number from string
   */
  private parseNumber(value: string | undefined, defaultValue?: number): number {
    if (value === undefined) {
      return defaultValue ?? 0;
    }
    const parsed = parseFloat(value);
    return isNaN(parsed) ? (defaultValue ?? 0) : parsed;
  }

  /**
   * Ensure value is an array
   */
  private ensureArray<T>(value: T | T[]): T[] {
    return Array.isArray(value) ? value : [value];
  }
}

/**
 * Parse XTCE XML content
 */
export function parseXTCE(xmlContent: string): ParsedXTCE {
  const parser = new XTCEParser();
  return parser.parse(xmlContent);
}

/**
 * Flatten nested space systems into a single parameter type map
 */
export function flattenParameterTypes(
  parsed: ParsedXTCE,
  prefix: string = ''
): Map<string, ParameterTypeInfo> {
  const result = new Map<string, ParameterTypeInfo>();
  const fullPrefix = prefix ? `${prefix}.` : '';

  // Add telemetry types
  for (const [name, type] of parsed.telemetry.parameterTypes) {
    result.set(`${fullPrefix}${name}`, type);
  }

  // Add command argument types
  for (const [name, type] of parsed.commands.argumentTypes) {
    result.set(`${fullPrefix}${name}`, type);
  }

  // Recurse into subsystems
  for (const subSystem of parsed.subSystems) {
    const subTypes = flattenParameterTypes(subSystem, `${fullPrefix}${subSystem.name}`);
    for (const [name, type] of subTypes) {
      result.set(name, type);
    }
  }

  return result;
}

/**
 * Flatten nested space systems into a single parameter list
 */
export function flattenParameters(
  parsed: ParsedXTCE,
  prefix: string = ''
): XTCEParameter[] {
  const fullPrefix = prefix ? `${prefix}.` : '';
  const result: XTCEParameter[] = [];

  // Add parameters with prefixed names
  for (const param of parsed.telemetry.parameters) {
    result.push({
      ...param,
      '@_name': `${fullPrefix}${param['@_name']}`,
    });
  }

  // Recurse into subsystems
  for (const subSystem of parsed.subSystems) {
    const subParams = flattenParameters(subSystem, `${fullPrefix}${subSystem.name}`);
    result.push(...subParams);
  }

  return result;
}
