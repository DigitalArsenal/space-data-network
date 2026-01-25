/**
 * Tests for the SDN Subscription module
 */

import { describe, it, expect, beforeEach } from 'vitest';
import {
  SubscriptionManager,
  SubscriptionConfig,
  QueryFilter,
  RoutingHeader,
  evaluateFilter,
  evaluateFilters,
  validateSubscriptionConfig,
  createDefaultConfig,
  generateSubscriptionId,
  serializeRoutingHeader,
  deserializeRoutingHeader,
  getSchemaRoutingTopic,
  getPeerRoutingTopic,
} from './subscription';

describe('SubscriptionManager', () => {
  let manager: SubscriptionManager;

  beforeEach(() => {
    manager = new SubscriptionManager();
  });

  describe('createSubscription', () => {
    it('should create a valid subscription', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['OMM.fbs', 'CDM.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      };

      const sub = manager.createSubscription(config);

      expect(sub.id).toMatch(/^sub_/);
      expect(sub.status).toBe('active');
      expect(sub.config.dataTypes).toEqual(['OMM.fbs', 'CDM.fbs']);
      expect(sub.messageCount).toBe(0);
    });

    it('should throw error for empty data types', () => {
      const config: SubscriptionConfig = {
        dataTypes: [],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      };

      expect(() => manager.createSubscription(config)).toThrow('At least one data type');
    });

    it('should throw error for invalid schema', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['INVALID.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      };

      expect(() => manager.createSubscription(config)).toThrow('Unknown data type');
    });
  });

  describe('subscription lifecycle', () => {
    it('should pause and resume subscriptions', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['OMM.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      };

      const sub = manager.createSubscription(config);
      expect(sub.status).toBe('active');

      manager.pauseSubscription(sub.id);
      expect(manager.getSubscription(sub.id)?.status).toBe('paused');

      manager.resumeSubscription(sub.id);
      expect(manager.getSubscription(sub.id)?.status).toBe('active');
    });

    it('should remove subscriptions', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['OMM.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      };

      const sub = manager.createSubscription(config);
      expect(manager.getSubscription(sub.id)).toBeDefined();

      manager.removeSubscription(sub.id);
      expect(manager.getSubscription(sub.id)).toBeUndefined();
    });
  });

  describe('processMessage', () => {
    it('should deliver matching messages', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['OMM.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      };

      const sub = manager.createSubscription(config);
      let receivedData: unknown = null;

      manager.addEventListener(sub.id, (event) => {
        if (event.type === 'message') {
          receivedData = event.data;
        }
      });

      const header: RoutingHeader = {
        schemaType: 'OMM',
        destinationPeers: [],
        ttl: 7,
        priority: 64,
        encrypted: true,
      };

      manager.processMessage('OMM.fbs', { OBJECT_NAME: 'ISS' }, 'peer1', header);

      expect(receivedData).toEqual({ OBJECT_NAME: 'ISS' });
      expect(manager.getSubscription(sub.id)?.messageCount).toBe(1);
    });

    it('should not deliver non-matching schema', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['OMM.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      };

      const sub = manager.createSubscription(config);
      let received = false;

      manager.addEventListener(sub.id, () => {
        received = true;
      });

      manager.processMessage('CDM.fbs', {}, 'peer1');

      expect(received).toBe(false);
    });

    it('should filter by source peer', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['OMM.fbs'],
        sourcePeers: ['peer1', 'peer2'],
        encrypted: true,
        streaming: true,
      };

      const sub = manager.createSubscription(config);
      const receivedFrom: string[] = [];

      manager.addEventListener(sub.id, (event) => {
        if (event.type === 'message' && event.from) {
          receivedFrom.push(event.from);
        }
      });

      manager.processMessage('OMM.fbs', {}, 'peer1');
      manager.processMessage('OMM.fbs', {}, 'peer2');
      manager.processMessage('OMM.fbs', {}, 'peer3'); // Should not match

      expect(receivedFrom).toEqual(['peer1', 'peer2']);
    });

    it('should apply field-level filters', () => {
      const config: SubscriptionConfig = {
        dataTypes: ['OMM.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
        filters: [
          { field: 'OBJECT_NAME', operator: 'eq', value: 'ISS' },
        ],
      };

      const sub = manager.createSubscription(config);
      let matchCount = 0;

      manager.addEventListener(sub.id, (event) => {
        if (event.type === 'message') {
          matchCount++;
        }
      });

      manager.processMessage('OMM.fbs', { OBJECT_NAME: 'ISS' }, 'peer1');
      manager.processMessage('OMM.fbs', { OBJECT_NAME: 'Hubble' }, 'peer1');

      expect(matchCount).toBe(1);
    });
  });

  describe('getRequiredTopics', () => {
    it('should return schema and peer topics', () => {
      manager.createSubscription({
        dataTypes: ['OMM.fbs', 'CDM.fbs'],
        sourcePeers: ['all'],
        encrypted: true,
        streaming: true,
      });

      manager.createSubscription({
        dataTypes: ['EPM.fbs'],
        sourcePeers: ['peer1'],
        encrypted: true,
        streaming: true,
      });

      const topics = manager.getRequiredTopics();

      expect(topics.has('/sdn/data/OMM')).toBe(true);
      expect(topics.has('/sdn/data/CDM')).toBe(true);
      expect(topics.has('/sdn/data/EPM')).toBe(true);
      expect(topics.has('/sdn/peer/peer1')).toBe(true);
    });
  });
});

describe('evaluateFilter', () => {
  it('should evaluate eq operator', () => {
    const data = { name: 'ISS' };
    expect(evaluateFilter(data, { field: 'name', operator: 'eq', value: 'ISS' })).toBe(true);
    expect(evaluateFilter(data, { field: 'name', operator: 'eq', value: 'Hubble' })).toBe(false);
  });

  it('should evaluate ne operator', () => {
    const data = { name: 'ISS' };
    expect(evaluateFilter(data, { field: 'name', operator: 'ne', value: 'Hubble' })).toBe(true);
    expect(evaluateFilter(data, { field: 'name', operator: 'ne', value: 'ISS' })).toBe(false);
  });

  it('should evaluate numeric operators', () => {
    const data = { altitude: 400 };
    expect(evaluateFilter(data, { field: 'altitude', operator: 'gt', value: 300 })).toBe(true);
    expect(evaluateFilter(data, { field: 'altitude', operator: 'gte', value: 400 })).toBe(true);
    expect(evaluateFilter(data, { field: 'altitude', operator: 'lt', value: 500 })).toBe(true);
    expect(evaluateFilter(data, { field: 'altitude', operator: 'lte', value: 400 })).toBe(true);
  });

  it('should evaluate string operators', () => {
    const data = { description: 'International Space Station' };
    expect(evaluateFilter(data, { field: 'description', operator: 'contains', value: 'Space' })).toBe(true);
    expect(evaluateFilter(data, { field: 'description', operator: 'startsWith', value: 'International' })).toBe(true);
    expect(evaluateFilter(data, { field: 'description', operator: 'endsWith', value: 'Station' })).toBe(true);
  });

  it('should evaluate in/notIn operators', () => {
    const data = { type: 'satellite' };
    expect(evaluateFilter(data, { field: 'type', operator: 'in', value: ['satellite', 'debris'] })).toBe(true);
    expect(evaluateFilter(data, { field: 'type', operator: 'notIn', value: ['debris', 'rocket'] })).toBe(true);
  });

  it('should handle nested fields', () => {
    const data = { object: { name: 'ISS', orbit: { altitude: 400 } } };
    expect(evaluateFilter(data, { field: 'object.name', operator: 'eq', value: 'ISS' })).toBe(true);
    expect(evaluateFilter(data, { field: 'object.orbit.altitude', operator: 'gt', value: 300 })).toBe(true);
  });
});

describe('evaluateFilters', () => {
  it('should return true for empty filters', () => {
    expect(evaluateFilters({}, [])).toBe(true);
  });

  it('should require all filters to match (AND logic)', () => {
    const data = { name: 'ISS', altitude: 400 };
    const filters: QueryFilter[] = [
      { field: 'name', operator: 'eq', value: 'ISS' },
      { field: 'altitude', operator: 'gt', value: 300 },
    ];
    expect(evaluateFilters(data, filters)).toBe(true);

    const failingFilters: QueryFilter[] = [
      { field: 'name', operator: 'eq', value: 'ISS' },
      { field: 'altitude', operator: 'lt', value: 300 }, // This fails
    ];
    expect(evaluateFilters(data, failingFilters)).toBe(false);
  });
});

describe('validateSubscriptionConfig', () => {
  it('should return no errors for valid config', () => {
    const config: SubscriptionConfig = {
      dataTypes: ['OMM.fbs'],
      sourcePeers: ['all'],
      encrypted: true,
      streaming: true,
    };
    expect(validateSubscriptionConfig(config)).toEqual([]);
  });

  it('should return error for empty dataTypes', () => {
    const config: SubscriptionConfig = {
      dataTypes: [],
      sourcePeers: ['all'],
      encrypted: true,
      streaming: true,
    };
    const errors = validateSubscriptionConfig(config);
    expect(errors.some(e => e.includes('data type'))).toBe(true);
  });

  it('should return error for invalid operator in filter', () => {
    const config: SubscriptionConfig = {
      dataTypes: ['OMM.fbs'],
      sourcePeers: ['all'],
      encrypted: true,
      streaming: true,
      filters: [{ field: 'test', operator: 'invalid' as any, value: 'x' }],
    };
    const errors = validateSubscriptionConfig(config);
    expect(errors.some(e => e.includes('invalid operator'))).toBe(true);
  });
});

describe('createDefaultConfig', () => {
  it('should create config with defaults', () => {
    const config = createDefaultConfig();
    expect(config.dataTypes).toEqual([]);
    expect(config.sourcePeers).toEqual(['all']);
    expect(config.encrypted).toBe(true);
    expect(config.streaming).toBe(true);
    expect(config.rateLimit).toBe(1000);
  });
});

describe('generateSubscriptionId', () => {
  it('should generate unique IDs', () => {
    const id1 = generateSubscriptionId();
    const id2 = generateSubscriptionId();
    expect(id1).not.toBe(id2);
    expect(id1).toMatch(/^sub_/);
    expect(id2).toMatch(/^sub_/);
  });
});

describe('RoutingHeader serialization', () => {
  it('should serialize and deserialize minimal header', () => {
    const header: RoutingHeader = {
      schemaType: 'OMM',
      destinationPeers: [],
      ttl: 7,
      priority: 64,
      encrypted: true,
    };

    const serialized = serializeRoutingHeader(header);
    const deserialized = deserializeRoutingHeader(serialized);

    expect(deserialized).not.toBeNull();
    expect(deserialized?.schemaType).toBe('OMM');
    expect(deserialized?.ttl).toBe(7);
    expect(deserialized?.priority).toBe(64);
    expect(deserialized?.encrypted).toBe(true);
  });

  it('should serialize and deserialize header with destinations', () => {
    const header: RoutingHeader = {
      schemaType: 'CDM',
      destinationPeers: ['peer1', 'peer2', 'peer3'],
      ttl: 5,
      priority: 128,
      encrypted: false,
    };

    const serialized = serializeRoutingHeader(header);
    const deserialized = deserializeRoutingHeader(serialized);

    expect(deserialized).not.toBeNull();
    expect(deserialized?.schemaType).toBe('CDM');
    expect(deserialized?.destinationPeers).toEqual(['peer1', 'peer2', 'peer3']);
    expect(deserialized?.ttl).toBe(5);
    expect(deserialized?.encrypted).toBe(false);
  });

  it('should serialize and deserialize header with session key', () => {
    const header: RoutingHeader = {
      schemaType: 'EPM',
      destinationPeers: [],
      ttl: 7,
      priority: 64,
      encrypted: true,
      sessionKeyId: 'session123',
    };

    const serialized = serializeRoutingHeader(header);
    const deserialized = deserializeRoutingHeader(serialized);

    expect(deserialized?.sessionKeyId).toBe('session123');
  });

  it('should return null for invalid data', () => {
    expect(deserializeRoutingHeader(new Uint8Array([]))).toBeNull();
    expect(deserializeRoutingHeader(new Uint8Array([0x03, 0x4f, 0x4d]))).toBeNull();
  });
});

describe('topic helpers', () => {
  it('should generate schema routing topic', () => {
    expect(getSchemaRoutingTopic('OMM.fbs')).toBe('/sdn/data/OMM');
    expect(getSchemaRoutingTopic('CDM.fbs')).toBe('/sdn/data/CDM');
  });

  it('should generate peer routing topic', () => {
    expect(getPeerRoutingTopic('QmXyz123')).toBe('/sdn/peer/QmXyz123');
  });
});
