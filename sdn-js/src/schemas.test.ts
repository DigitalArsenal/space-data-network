import { describe, it, expect } from 'vitest';
import {
  SUPPORTED_SCHEMAS,
  SCHEMA_DESCRIPTIONS,
  getTopicName,
  getSchemaFromTopic,
  isValidSchema,
  type SchemaName,
} from './schemas';

// Mirrors sdn-server/internal/sds: 161 Space Data Standards schemas
// plus 4 SDN-internal schemas (PGR, PLHD, PLOG, RHD).
const EXPECTED_STANDARD_SCHEMA_COUNT = 161;
const INTERNAL_SCHEMAS = ['PGR.fbs', 'PLHD.fbs', 'PLOG.fbs', 'RHD.fbs'] as const;
const EXPECTED_TOTAL_SCHEMA_COUNT =
  EXPECTED_STANDARD_SCHEMA_COUNT + INTERNAL_SCHEMAS.length;

describe('schemas', () => {
  describe('SUPPORTED_SCHEMAS', () => {
    it('should contain expected SDS schemas', () => {
      expect(SUPPORTED_SCHEMAS).toContain('OMM.fbs');
      expect(SUPPORTED_SCHEMAS).toContain('CDM.fbs');
      expect(SUPPORTED_SCHEMAS).toContain('EPM.fbs');
      expect(SUPPORTED_SCHEMAS).toContain('PNM.fbs');
      expect(SUPPORTED_SCHEMAS).toContain('OEM.fbs');
      expect(SUPPORTED_SCHEMAS).toContain('SPW.fbs');
    });

    it('should register the full schema set (standard + SDN-internal)', () => {
      expect(SUPPORTED_SCHEMAS.length).toBe(EXPECTED_TOTAL_SCHEMA_COUNT);

      for (const internal of INTERNAL_SCHEMAS) {
        expect(SUPPORTED_SCHEMAS).toContain(internal);
      }

      const standardCount = SUPPORTED_SCHEMAS.filter(
        (s) => !INTERNAL_SCHEMAS.includes(s as (typeof INTERNAL_SCHEMAS)[number]),
      ).length;
      expect(standardCount).toBe(EXPECTED_STANDARD_SCHEMA_COUNT);
    });

    it('should have unique values', () => {
      const uniqueSchemas = new Set(SUPPORTED_SCHEMAS);
      expect(uniqueSchemas.size).toBe(SUPPORTED_SCHEMAS.length);
    });

    it('should have all schemas with .fbs extension', () => {
      for (const schema of SUPPORTED_SCHEMAS) {
        expect(schema).toMatch(/\.fbs$/);
      }
    });
  });

  describe('SCHEMA_DESCRIPTIONS', () => {
    it('should have descriptions for all supported schemas', () => {
      for (const schema of SUPPORTED_SCHEMAS) {
        expect(SCHEMA_DESCRIPTIONS[schema]).toBeDefined();
        expect(SCHEMA_DESCRIPTIONS[schema].length).toBeGreaterThan(0);
      }
    });

    it('should contain meaningful descriptions', () => {
      expect(SCHEMA_DESCRIPTIONS['OMM.fbs']).toContain('Orbit');
      expect(SCHEMA_DESCRIPTIONS['CDM.fbs']).toContain('Conjunction');
      expect(SCHEMA_DESCRIPTIONS['EPM.fbs']).toContain('Entity');
      expect(SCHEMA_DESCRIPTIONS['SPW.fbs']).toContain('Space Weather');
    });
  });

  describe('getTopicName', () => {
    it('should generate public standardCode topic names', () => {
      expect(getTopicName('OMM.fbs')).toBe('/spacedatanetwork/sds/OMM');
      expect(getTopicName('CDM.fbs')).toBe('/spacedatanetwork/sds/CDM');
      expect(getTopicName('EPM.fbs')).toBe('/spacedatanetwork/sds/EPM');
    });

    it('should follow the standardCode topic format', () => {
      const topic = getTopicName('PNM.fbs');
      expect(topic).toMatch(/^\/spacedatanetwork\/sds\/[A-Z]{3}$/);
    });
  });

  describe('getSchemaFromTopic', () => {
    it('should extract internal schema from public standardCode topic', () => {
      expect(getSchemaFromTopic('/spacedatanetwork/sds/OMM')).toBe('OMM.fbs');
      expect(getSchemaFromTopic('/spacedatanetwork/sds/CDM')).toBe('CDM.fbs');
      expect(getSchemaFromTopic('/spacedatanetwork/sds/EPM')).toBe('EPM.fbs');
    });

    it('should return null for invalid topic format', () => {
      expect(getSchemaFromTopic('/invalid/topic')).toBeNull();
      expect(getSchemaFromTopic('/spacedatanetwork/wrong/OMM.fbs')).toBeNull();
      expect(getSchemaFromTopic('')).toBeNull();
    });

    it('should return null for unknown schema', () => {
      expect(getSchemaFromTopic('/spacedatanetwork/sds/UNKNOWN')).toBeNull();
    });

    it('should be inverse of getTopicName', () => {
      for (const schema of SUPPORTED_SCHEMAS) {
        const topic = getTopicName(schema);
        const extracted = getSchemaFromTopic(topic);
        expect(extracted).toBe(schema);
      }
    });
  });

  describe('isValidSchema', () => {
    it('should return true for valid schemas', () => {
      expect(isValidSchema('OMM.fbs')).toBe(true);
      expect(isValidSchema('CDM.fbs')).toBe(true);
      expect(isValidSchema('EPM.fbs')).toBe(true);
      expect(isValidSchema('PNM.fbs')).toBe(true);
    });

    it('should return false for invalid schemas', () => {
      expect(isValidSchema('UNKNOWN.fbs')).toBe(false);
      expect(isValidSchema('OMM')).toBe(false);
      expect(isValidSchema('')).toBe(false);
      expect(isValidSchema('omm.fbs')).toBe(false); // case sensitive
    });

    it('should validate all supported schemas', () => {
      for (const schema of SUPPORTED_SCHEMAS) {
        expect(isValidSchema(schema)).toBe(true);
      }
    });
  });
});
