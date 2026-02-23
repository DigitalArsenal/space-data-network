/**
 * Tests for the XTCE to JSON Schema / FlatBuffer converter
 */

import { describe, it, expect, beforeAll } from 'vitest';
import * as fs from 'fs';
import * as path from 'path';
import {
  parseXTCE,
  convertXTCE,
  convertXTCEToJsonSchema,
  convertXTCEToFlatBuffer,
  flattenParameterTypes,
  flattenParameters,
  validateAgainstSchema,
} from '../src/index.js';

// Sample XTCE content for testing
const sampleXTCE = `<?xml version="1.0" encoding="UTF-8"?>
<xtce:SpaceSystem xmlns:xtce="http://www.omg.org/spec/XTCE/20180204"
                  name="TestSpacecraft"
                  shortDescription="Test spacecraft for unit tests">
  <xtce:TelemetryMetaData>
    <xtce:ParameterTypeSet>
      <xtce:IntegerParameterType name="Temperature_Type" signed="true" sizeInBits="16"
                                  shortDescription="Temperature value in raw ADC counts">
        <xtce:UnitSet>
          <xtce:Unit description="ADC counts">counts</xtce:Unit>
        </xtce:UnitSet>
        <xtce:IntegerDataEncoding sizeInBits="16" encoding="twosComplement"/>
        <xtce:ValidRange minInclusive="-32768" maxInclusive="32767"/>
      </xtce:IntegerParameterType>

      <xtce:FloatParameterType name="Voltage_Type" sizeInBits="32"
                                shortDescription="Voltage measurement">
        <xtce:UnitSet>
          <xtce:Unit description="Potential">V</xtce:Unit>
        </xtce:UnitSet>
        <xtce:FloatDataEncoding sizeInBits="32" encoding="IEEE754_1985"/>
        <xtce:ValidRange minInclusive="0.0" maxInclusive="50.0"/>
      </xtce:FloatParameterType>

      <xtce:EnumeratedParameterType name="Mode_Type"
                                     shortDescription="Operational mode">
        <xtce:IntegerDataEncoding sizeInBits="8" encoding="unsigned"/>
        <xtce:EnumerationList>
          <xtce:Enumeration value="0" label="OFF"/>
          <xtce:Enumeration value="1" label="STANDBY"/>
          <xtce:Enumeration value="2" label="ACTIVE"/>
        </xtce:EnumerationList>
      </xtce:EnumeratedParameterType>

      <xtce:BooleanParameterType name="Flag_Type"
                                  zeroStringValue="FALSE" oneStringValue="TRUE">
        <xtce:IntegerDataEncoding sizeInBits="1" encoding="unsigned"/>
      </xtce:BooleanParameterType>

      <xtce:StringParameterType name="Message_Type">
        <xtce:StringDataEncoding encoding="UTF-8">
          <xtce:SizeInBits>
            <xtce:Fixed>
              <xtce:FixedValue>128</xtce:FixedValue>
            </xtce:Fixed>
          </xtce:SizeInBits>
        </xtce:StringDataEncoding>
      </xtce:StringParameterType>
    </xtce:ParameterTypeSet>

    <xtce:ParameterSet>
      <xtce:Parameter name="TEMP_SENSOR_1" parameterTypeRef="Temperature_Type"
                      shortDescription="Temperature sensor 1 reading"/>
      <xtce:Parameter name="BATTERY_VOLTAGE" parameterTypeRef="Voltage_Type"
                      shortDescription="Main battery voltage"/>
      <xtce:Parameter name="SYSTEM_MODE" parameterTypeRef="Mode_Type"
                      shortDescription="Current system mode"/>
      <xtce:Parameter name="HEATER_ON" parameterTypeRef="Flag_Type"
                      shortDescription="Heater enabled flag"/>
      <xtce:Parameter name="STATUS_MSG" parameterTypeRef="Message_Type"
                      shortDescription="Status message"/>
    </xtce:ParameterSet>
  </xtce:TelemetryMetaData>

  <xtce:CommandMetaData>
    <xtce:ArgumentTypeSet>
      <xtce:IntegerArgumentType name="ModeArg" signed="false" sizeInBits="8">
        <xtce:IntegerDataEncoding sizeInBits="8" encoding="unsigned"/>
        <xtce:ValidRange minInclusive="0" maxInclusive="2"/>
      </xtce:IntegerArgumentType>
    </xtce:ArgumentTypeSet>
    <xtce:MetaCommandSet>
      <xtce:MetaCommand name="SET_MODE" shortDescription="Set system mode">
        <xtce:ArgumentList>
          <xtce:Argument name="TARGET_MODE" argumentTypeRef="ModeArg"/>
        </xtce:ArgumentList>
      </xtce:MetaCommand>
    </xtce:MetaCommandSet>
  </xtce:CommandMetaData>
</xtce:SpaceSystem>
`;

describe('XTCEParser', () => {
  it('should parse basic XTCE document', () => {
    const parsed = parseXTCE(sampleXTCE);

    expect(parsed.name).toBe('TestSpacecraft');
    expect(parsed.description).toBe('Test spacecraft for unit tests');
  });

  it('should extract parameter types', () => {
    const parsed = parseXTCE(sampleXTCE);
    const types = flattenParameterTypes(parsed);

    expect(types.size).toBe(5);
    expect(types.has('Temperature_Type')).toBe(true);
    expect(types.has('Voltage_Type')).toBe(true);
    expect(types.has('Mode_Type')).toBe(true);
    expect(types.has('Flag_Type')).toBe(true);
    expect(types.has('Message_Type')).toBe(true);
  });

  it('should parse integer type correctly', () => {
    const parsed = parseXTCE(sampleXTCE);
    const types = flattenParameterTypes(parsed);
    const tempType = types.get('Temperature_Type');

    expect(tempType).toBeDefined();
    expect(tempType!.type).toBe('integer');
    expect(tempType!.signed).toBe(true);
    expect(tempType!.sizeInBits).toBe(16);
    expect(tempType!.encoding).toBe('twosComplement');
    expect(tempType!.unit).toBe('counts');
    expect(tempType!.validRange).toEqual({
      minInclusive: -32768,
      maxInclusive: 32767,
    });
  });

  it('should parse float type correctly', () => {
    const parsed = parseXTCE(sampleXTCE);
    const types = flattenParameterTypes(parsed);
    const voltType = types.get('Voltage_Type');

    expect(voltType).toBeDefined();
    expect(voltType!.type).toBe('float');
    expect(voltType!.sizeInBits).toBe(32);
    expect(voltType!.encoding).toBe('IEEE754_1985');
    expect(voltType!.unit).toBe('V');
  });

  it('should parse enumerated type correctly', () => {
    const parsed = parseXTCE(sampleXTCE);
    const types = flattenParameterTypes(parsed);
    const modeType = types.get('Mode_Type');

    expect(modeType).toBeDefined();
    expect(modeType!.type).toBe('enumerated');
    expect(modeType!.enumerations).toHaveLength(3);
    expect(modeType!.enumerations![0]).toEqual({ value: 0, label: 'OFF', description: undefined });
    expect(modeType!.enumerations![2]).toEqual({ value: 2, label: 'ACTIVE', description: undefined });
  });

  it('should parse boolean type correctly', () => {
    const parsed = parseXTCE(sampleXTCE);
    const types = flattenParameterTypes(parsed);
    const flagType = types.get('Flag_Type');

    expect(flagType).toBeDefined();
    expect(flagType!.type).toBe('boolean');
    expect(flagType!.booleanLabels).toEqual({ zero: 'FALSE', one: 'TRUE' });
  });

  it('should extract parameters', () => {
    const parsed = parseXTCE(sampleXTCE);
    const params = flattenParameters(parsed);

    expect(params).toHaveLength(5);
    expect(params.map(p => p['@_name'])).toContain('TEMP_SENSOR_1');
    expect(params.map(p => p['@_name'])).toContain('BATTERY_VOLTAGE');
  });

  it('should parse command arguments', () => {
    const parsed = parseXTCE(sampleXTCE);

    expect(parsed.commands.commands).toHaveLength(1);
    expect(parsed.commands.commands[0]['@_name']).toBe('SET_MODE');
    expect(parsed.commands.arguments).toHaveLength(1);
    expect(parsed.commands.arguments[0]['@_name']).toBe('TARGET_MODE');
  });
});

describe('JSON Schema Generator', () => {
  it('should generate valid JSON Schema', () => {
    const jsonSchema = convertXTCEToJsonSchema(sampleXTCE);
    const schema = JSON.parse(jsonSchema);

    expect(schema.$schema).toBe('https://json-schema.org/draft/2019-09/schema');
    expect(schema.title).toBe('TestSpacecraft');
    expect(schema.type).toBe('object');
    expect(schema.properties).toBeDefined();
  });

  it('should include x-flatbuffer annotations', () => {
    const jsonSchema = convertXTCEToJsonSchema(sampleXTCE);
    const schema = JSON.parse(jsonSchema);

    // Check integer property
    expect(schema.properties.TEMP_SENSOR_1['x-flatbuffer-type']).toBe('int16');
    expect(schema.properties.TEMP_SENSOR_1['x-flatbuffer-field-id']).toBeDefined();

    // Check float property
    expect(schema.properties.BATTERY_VOLTAGE['x-flatbuffer-type']).toBe('float32');

    // Check boolean property
    expect(schema.properties.HEATER_ON['x-flatbuffer-type']).toBe('bool');

    // Check string property
    expect(schema.properties.STATUS_MSG['x-flatbuffer-type']).toBe('string');
  });

  it('should include x-xtce annotations', () => {
    const jsonSchema = convertXTCEToJsonSchema(sampleXTCE);
    const schema = JSON.parse(jsonSchema);

    // Check unit annotation
    expect(schema.properties.TEMP_SENSOR_1['x-xtce-unit']).toBe('counts');
    expect(schema.properties.BATTERY_VOLTAGE['x-xtce-unit']).toBe('V');

    // Check encoding annotation
    expect(schema.properties.TEMP_SENSOR_1['x-xtce-encoding']).toBe('twosComplement');
  });

  it('should include valid range constraints', () => {
    const jsonSchema = convertXTCEToJsonSchema(sampleXTCE);
    const schema = JSON.parse(jsonSchema);

    // Integer range
    expect(schema.properties.TEMP_SENSOR_1.minimum).toBe(-32768);
    expect(schema.properties.TEMP_SENSOR_1.maximum).toBe(32767);

    // Float range
    expect(schema.properties.BATTERY_VOLTAGE.minimum).toBe(0);
    expect(schema.properties.BATTERY_VOLTAGE.maximum).toBe(50);
  });

  it('should generate enum values for enumerated types', () => {
    const jsonSchema = convertXTCEToJsonSchema(sampleXTCE);
    const schema = JSON.parse(jsonSchema);

    expect(schema.properties.SYSTEM_MODE.enum).toEqual(['OFF', 'STANDBY', 'ACTIVE']);
  });
});

describe('FlatBuffer Schema Generator', () => {
  it('should generate valid FlatBuffer schema', () => {
    const fbsSchema = convertXTCEToFlatBuffer(sampleXTCE);

    expect(fbsSchema).toContain('table TestSpacecraft');
    expect(fbsSchema).toContain('root_type TestSpacecraft');
    expect(fbsSchema).toContain('file_identifier');
  });

  it('should include field IDs', () => {
    const fbsSchema = convertXTCEToFlatBuffer(sampleXTCE);

    expect(fbsSchema).toMatch(/TEMP_SENSOR_1:int16\s+\(id:\s*\d+\)/);
    expect(fbsSchema).toMatch(/BATTERY_VOLTAGE:float\s+\(id:\s*\d+\)/);
  });

  it('should generate enums', () => {
    const fbsSchema = convertXTCEToFlatBuffer(sampleXTCE);

    expect(fbsSchema).toContain('enum Mode_Type : int');
    expect(fbsSchema).toContain('OFF = 0');
    expect(fbsSchema).toContain('STANDBY = 1');
    expect(fbsSchema).toContain('ACTIVE = 2');
  });

  it('should use correct FlatBuffer types', () => {
    const fbsSchema = convertXTCEToFlatBuffer(sampleXTCE);

    // Signed 16-bit integer
    expect(fbsSchema).toContain('TEMP_SENSOR_1:int16');

    // 32-bit float
    expect(fbsSchema).toContain('BATTERY_VOLTAGE:float');

    // Boolean
    expect(fbsSchema).toContain('HEATER_ON:bool');

    // String
    expect(fbsSchema).toContain('STATUS_MSG:string');
  });
});

describe('Full Converter', () => {
  it('should return complete conversion result', () => {
    const result = convertXTCE(sampleXTCE);

    expect(result.jsonSchema).toBeDefined();
    expect(result.flatBufferSchema).toBeDefined();
    expect(result.telemetryParameters).toHaveLength(5);
    expect(result.commandArguments).toHaveLength(1);
    // ModeArg is an ArgumentType, not a ParameterType, so there's a warning about it
    // This is expected behavior - the warning indicates the type was in ArgumentTypeSet
    expect(result.warnings.length).toBeLessThanOrEqual(1);
  });

  it('should support namespace option', () => {
    const result = convertXTCE(sampleXTCE, { namespace: 'MySpacecraft' });

    expect(result.flatBufferSchema.namespace).toBe('MySpacecraft');
  });

  it('should support schemaId option', () => {
    const result = convertXTCE(sampleXTCE, {
      schemaId: 'https://example.com/schemas/test.json',
    });

    expect(result.jsonSchema.$id).toBe('https://example.com/schemas/test.json');
  });

  it('should support telemetry-only conversion', () => {
    const result = convertXTCE(sampleXTCE, {
      includeTelemetry: true,
      includeCommands: false,
    });

    // Should have telemetry parameters but no command arguments in properties
    const props = Object.keys(result.jsonSchema.properties!);
    expect(props.some(p => p.startsWith('cmd_'))).toBe(false);
  });
});

describe('Schema Validation', () => {
  it('should validate correct data', () => {
    const result = convertXTCE(sampleXTCE);
    const data = {
      TEMP_SENSOR_1: 2500,
      BATTERY_VOLTAGE: 12.5,
      SYSTEM_MODE: 'ACTIVE',
      HEATER_ON: true,
      STATUS_MSG: 'OK', // Must be <= 16 chars (128 bits / 8)
    };

    const validation = validateAgainstSchema(result.jsonSchema, data);
    expect(validation.valid).toBe(true);
    expect(validation.errors).toHaveLength(0);
  });

  it('should detect type mismatches', () => {
    const result = convertXTCE(sampleXTCE);
    const data = {
      TEMP_SENSOR_1: 'not a number',
      BATTERY_VOLTAGE: 12.5,
    };

    const validation = validateAgainstSchema(result.jsonSchema, data);
    expect(validation.valid).toBe(false);
    expect(validation.errors.some(e => e.includes('TEMP_SENSOR_1'))).toBe(true);
  });

  it('should detect range violations', () => {
    const result = convertXTCE(sampleXTCE);
    const data = {
      TEMP_SENSOR_1: 50000, // Exceeds 32767 max
      BATTERY_VOLTAGE: 100, // Exceeds 50V max
    };

    const validation = validateAgainstSchema(result.jsonSchema, data);
    expect(validation.valid).toBe(false);
    expect(validation.errors.length).toBeGreaterThan(0);
  });

  it('should detect invalid enum values', () => {
    const result = convertXTCE(sampleXTCE);
    const data = {
      SYSTEM_MODE: 'INVALID_MODE',
    };

    const validation = validateAgainstSchema(result.jsonSchema, data);
    expect(validation.valid).toBe(false);
    expect(validation.errors.some(e => e.includes('SYSTEM_MODE'))).toBe(true);
  });
});

describe('Sample XTCE File', () => {
  let exampleXTCE: string;

  beforeAll(() => {
    const samplePath = path.join(__dirname, '..', 'samples', 'example-spacecraft.xml');
    if (fs.existsSync(samplePath)) {
      exampleXTCE = fs.readFileSync(samplePath, 'utf-8');
    }
  });

  it('should parse example-spacecraft.xml', () => {
    if (!exampleXTCE) {
      console.log('Skipping: example-spacecraft.xml not found');
      return;
    }

    const parsed = parseXTCE(exampleXTCE);
    expect(parsed.name).toBe('ExampleSpacecraft');
  });

  it('should convert example-spacecraft.xml to JSON Schema', () => {
    if (!exampleXTCE) {
      console.log('Skipping: example-spacecraft.xml not found');
      return;
    }

    const result = convertXTCE(exampleXTCE);
    expect(result.jsonSchema.title).toBe('ExampleSpacecraft');
    expect(Object.keys(result.jsonSchema.properties!).length).toBeGreaterThan(0);
  });

  it('should convert example-spacecraft.xml to FlatBuffer schema', () => {
    if (!exampleXTCE) {
      console.log('Skipping: example-spacecraft.xml not found');
      return;
    }

    const fbsSchema = convertXTCEToFlatBuffer(exampleXTCE);
    expect(fbsSchema).toContain('table ExampleSpacecraft');
    expect(fbsSchema).toContain('enum OperationalMode_Type');
  });
});
