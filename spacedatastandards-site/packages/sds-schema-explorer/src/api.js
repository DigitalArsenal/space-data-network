/**
 * Space Data Standards - Schema Registry API
 * Simulated API endpoints for schema discovery, retrieval, validation, and code generation
 */

import { SCHEMAS } from './schemas.js';
import { generateJsonSchema, generateCode } from './generators.js';

/**
 * Simulated API endpoints - these functions provide the same interface
 * as the planned server-side API, running entirely in the browser.
 */
export const SchemaRegistryAPI = {
  /** GET /api/schemas - List all schemas */
  listSchemas() {
    return SCHEMAS.map(s => ({
      name: s.name,
      fullName: s.fullName,
      description: s.description,
      version: s.version,
      category: s.category,
      fieldCount: s.fields.length,
      formats: ['json-schema', 'flatbuffers', 'typescript', 'go', 'python', 'rust'],
      links: {
        json_schema: `/api/schemas/${s.name}/json-schema`,
        flatbuffers: `/api/schemas/${s.name}/flatbuffers`,
        typescript: `/api/schemas/${s.name}/typescript`,
      }
    }));
  },

  /** GET /api/schemas/{name} - Get schema metadata */
  getSchema(name) {
    const s = SCHEMAS.find(x => x.name === name.toUpperCase());
    if (!s) return { error: 'Schema not found', status: 404 };
    return {
      name: s.name,
      fullName: s.fullName,
      description: s.description,
      version: s.version,
      category: s.category,
      fieldCount: s.fields.length,
      includes: s.includes,
      fileIdentifier: s.fileIdentifier,
      formats: ['json-schema', 'flatbuffers', 'typescript', 'go', 'python', 'rust'],
    };
  },

  /** GET /api/schemas/{name}/json-schema - Get JSON Schema */
  getJsonSchema(name) {
    const s = SCHEMAS.find(x => x.name === name.toUpperCase());
    if (!s) return { error: 'Schema not found', status: 404 };
    return generateJsonSchema(s);
  },

  /** POST /api/validate - Validate data against schema */
  validate(schemaName, data) {
    const s = SCHEMAS.find(x => x.name === schemaName.toUpperCase());
    if (!s) return { valid: false, errors: ['Schema not found'], schema: schemaName };

    const errors = [];
    const requiredFields = s.fields.filter(f => f.required);
    requiredFields.forEach(f => {
      if (!(f.name in data)) {
        errors.push(`Missing required field: ${f.name}`);
      }
    });

    // Type checking
    Object.entries(data).forEach(([key, value]) => {
      const field = s.fields.find(f => f.name === key);
      if (!field) {
        errors.push(`Unknown field: ${key}`);
        return;
      }
      const expectedType = field.type;
      const actualType = Array.isArray(value) ? 'array' : typeof value;
      if (expectedType === 'number' && actualType !== 'number') {
        errors.push(`Field ${key}: expected number, got ${actualType}`);
      } else if (expectedType === 'integer' && (!Number.isInteger(value))) {
        errors.push(`Field ${key}: expected integer, got ${actualType}`);
      } else if (expectedType === 'string' && actualType !== 'string') {
        errors.push(`Field ${key}: expected string, got ${actualType}`);
      }
    });

    return {
      valid: errors.length === 0,
      errors,
      schema: schemaName,
      version: s.version,
    };
  },

  /** POST /api/generate - Generate code from schema */
  generate(schemaName, format) {
    const s = SCHEMAS.find(x => x.name === schemaName.toUpperCase());
    if (!s) return { error: 'Schema not found' };
    return { code: generateCode(s, format), format, schema: schemaName };
  },
};
