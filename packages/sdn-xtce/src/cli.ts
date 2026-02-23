#!/usr/bin/env node
/**
 * XTCE to JSON Schema / FlatBuffer CLI
 *
 * Usage:
 *   sdn-xtce convert --input spacecraft.xml --output-schema spacecraft.schema.json --output-fbs spacecraft.fbs
 */

import { Command } from 'commander';
import * as fs from 'fs';
import * as path from 'path';
import {
  convertXTCE,
  getConversionSummary,
} from './converter.js';
import { serializeFlatBufferSchema } from './flatbuffer-generator.js';

const program = new Command();

program
  .name('sdn-xtce')
  .description('XTCE (XML Telemetry/Command Exchange) to JSON Schema / FlatBuffer converter')
  .version('1.0.0');

program
  .command('convert')
  .description('Convert XTCE XML to JSON Schema and/or FlatBuffer schema')
  .requiredOption('-i, --input <file>', 'Input XTCE XML file')
  .option('-s, --output-schema <file>', 'Output JSON Schema file')
  .option('-f, --output-fbs <file>', 'Output FlatBuffer schema file')
  .option('-n, --namespace <namespace>', 'FlatBuffer namespace')
  .option('--no-telemetry', 'Exclude telemetry parameters')
  .option('--no-commands', 'Exclude command definitions')
  .option('--no-enums', 'Do not generate FlatBuffer enums')
  .option('--field-id-offset <number>', 'Starting field ID offset', '0')
  .option('--schema-id <uri>', 'JSON Schema $id URI')
  .option('-q, --quiet', 'Suppress output messages')
  .option('-v, --verbose', 'Show detailed output')
  .action(async (options) => {
    try {
      // Read input file
      const inputPath = path.resolve(options.input);
      if (!fs.existsSync(inputPath)) {
        console.error(`Error: Input file not found: ${inputPath}`);
        process.exit(1);
      }

      const xmlContent = fs.readFileSync(inputPath, 'utf-8');

      if (!options.quiet) {
        console.log(`Reading XTCE from: ${inputPath}`);
      }

      // Convert
      const result = convertXTCE(xmlContent, {
        namespace: options.namespace,
        schemaId: options.schemaId,
        includeTelemetry: options.telemetry,
        includeCommands: options.commands,
        generateEnums: options.enums,
        fieldIdOffset: parseInt(options.fieldIdOffset, 10),
      });

      // Show summary
      if (options.verbose) {
        const summary = getConversionSummary(result);
        console.log('\nConversion Summary:');
        console.log(`  Telemetry Parameters: ${summary.telemetryCount}`);
        console.log(`  Command Arguments: ${summary.commandCount}`);
        console.log(`  JSON Schema Properties: ${summary.propertyCount}`);
        console.log(`  FlatBuffer Enums: ${summary.enumCount}`);
        console.log(`  Warnings: ${summary.warningCount}`);
      }

      // Show warnings
      if (result.warnings.length > 0 && !options.quiet) {
        console.log('\nWarnings:');
        for (const warning of result.warnings) {
          console.log(`  - ${warning}`);
        }
      }

      // Write JSON Schema
      if (options.outputSchema) {
        const schemaPath = path.resolve(options.outputSchema);
        const schemaJson = JSON.stringify(result.jsonSchema, null, 2);
        fs.writeFileSync(schemaPath, schemaJson, 'utf-8');
        if (!options.quiet) {
          console.log(`\nJSON Schema written to: ${schemaPath}`);
        }
      }

      // Write FlatBuffer Schema
      if (options.outputFbs) {
        const fbsPath = path.resolve(options.outputFbs);
        const fbsContent = serializeFlatBufferSchema(result.flatBufferSchema);
        fs.writeFileSync(fbsPath, fbsContent, 'utf-8');
        if (!options.quiet) {
          console.log(`FlatBuffer schema written to: ${fbsPath}`);
        }
      }

      // If no output specified, print JSON Schema to stdout
      if (!options.outputSchema && !options.outputFbs) {
        console.log(JSON.stringify(result.jsonSchema, null, 2));
      }

      if (!options.quiet) {
        console.log('\nConversion complete!');
      }
    } catch (error) {
      console.error(`Error: ${error instanceof Error ? error.message : String(error)}`);
      process.exit(1);
    }
  });

program
  .command('validate')
  .description('Validate JSON data against a generated schema')
  .requiredOption('-s, --schema <file>', 'JSON Schema file')
  .requiredOption('-d, --data <file>', 'JSON data file to validate')
  .option('-q, --quiet', 'Only output errors')
  .action(async (options) => {
    try {
      const schemaPath = path.resolve(options.schema);
      const dataPath = path.resolve(options.data);

      if (!fs.existsSync(schemaPath)) {
        console.error(`Error: Schema file not found: ${schemaPath}`);
        process.exit(1);
      }

      if (!fs.existsSync(dataPath)) {
        console.error(`Error: Data file not found: ${dataPath}`);
        process.exit(1);
      }

      const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf-8'));
      const data = JSON.parse(fs.readFileSync(dataPath, 'utf-8'));

      // Import and use validator
      const { validateAgainstSchema } = await import('./converter.js');
      const result = validateAgainstSchema(schema, data);

      if (result.valid) {
        if (!options.quiet) {
          console.log('Validation passed!');
        }
        process.exit(0);
      } else {
        console.error('Validation failed:');
        for (const error of result.errors) {
          console.error(`  - ${error}`);
        }
        process.exit(1);
      }
    } catch (error) {
      console.error(`Error: ${error instanceof Error ? error.message : String(error)}`);
      process.exit(1);
    }
  });

program
  .command('info')
  .description('Show information about an XTCE file')
  .requiredOption('-i, --input <file>', 'Input XTCE XML file')
  .option('-v, --verbose', 'Show detailed information')
  .action(async (options) => {
    try {
      const inputPath = path.resolve(options.input);
      if (!fs.existsSync(inputPath)) {
        console.error(`Error: Input file not found: ${inputPath}`);
        process.exit(1);
      }

      const xmlContent = fs.readFileSync(inputPath, 'utf-8');

      // Parse XTCE
      const { parseXTCE, flattenParameterTypes, flattenParameters } = await import('./parser.js');
      const parsed = parseXTCE(xmlContent);

      console.log('XTCE Document Information:');
      console.log('==========================');
      console.log(`Name: ${parsed.name}`);
      if (parsed.description) {
        console.log(`Description: ${parsed.description}`);
      }
      console.log('');

      // Telemetry
      const allTypes = flattenParameterTypes(parsed);
      const allParams = flattenParameters(parsed);

      console.log('Telemetry:');
      console.log(`  Parameter Types: ${allTypes.size}`);
      console.log(`  Parameters: ${allParams.length}`);
      console.log('');

      // Commands
      console.log('Commands:');
      console.log(`  Argument Types: ${parsed.commands.argumentTypes.size}`);
      console.log(`  Arguments: ${parsed.commands.arguments.length}`);
      console.log(`  Meta Commands: ${parsed.commands.commands.length}`);
      console.log('');

      // Subsystems
      if (parsed.subSystems.length > 0) {
        console.log(`Subsystems: ${parsed.subSystems.length}`);
        for (const sub of parsed.subSystems) {
          console.log(`  - ${sub.name}`);
        }
        console.log('');
      }

      // Verbose details
      if (options.verbose) {
        console.log('Parameter Types:');
        for (const [name, type] of allTypes) {
          console.log(`  ${name}: ${type.type}${type.sizeInBits ? ` (${type.sizeInBits} bits)` : ''}`);
        }
        console.log('');

        console.log('Parameters:');
        for (const param of allParams) {
          console.log(`  ${param['@_name']} -> ${param['@_parameterTypeRef']}`);
        }
      }
    } catch (error) {
      console.error(`Error: ${error instanceof Error ? error.message : String(error)}`);
      process.exit(1);
    }
  });

program.parse();
