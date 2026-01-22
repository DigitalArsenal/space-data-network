/**
 * SDN Storage - IndexedDB-based local storage for SDS data
 */

import { openDB, IDBPDatabase, DBSchema } from 'idb';
import { SchemaName, SUPPORTED_SCHEMAS } from './schemas';

interface SDNDBSchema extends DBSchema {
  records: {
    key: string; // cid
    value: StoredRecord;
    indexes: {
      'by-schema': string;
      'by-peer': string;
      'by-time': number;
      'by-schema-peer': [string, string];
    };
  };
  metadata: {
    key: string;
    value: unknown;
  };
}

export interface StoredRecord {
  cid: string;
  schema: string;
  peerId: string;
  timestamp: number;
  data: Uint8Array;
  signature: Uint8Array;
}

export interface QueryFilter {
  peerId?: string;
  since?: Date;
  limit?: number;
}

export class SDNStorage {
  private db: IDBPDatabase<SDNDBSchema> | null = null;
  private dbName: string;

  private constructor(dbName: string) {
    this.dbName = dbName;
  }

  /**
   * Open or create the storage database
   */
  static async open(dbName: string = 'sdn-store'): Promise<SDNStorage> {
    const storage = new SDNStorage(dbName);
    await storage.init();
    return storage;
  }

  private async init(): Promise<void> {
    this.db = await openDB<SDNDBSchema>(this.dbName, 1, {
      upgrade(db) {
        // Create records store with indexes
        const recordsStore = db.createObjectStore('records', { keyPath: 'cid' });
        recordsStore.createIndex('by-schema', 'schema');
        recordsStore.createIndex('by-peer', 'peerId');
        recordsStore.createIndex('by-time', 'timestamp');
        recordsStore.createIndex('by-schema-peer', ['schema', 'peerId']);

        // Create metadata store
        db.createObjectStore('metadata');
      },
    });
  }

  /**
   * Store a record
   */
  async store(
    schema: SchemaName,
    data: Uint8Array,
    peerId: string,
    signature: Uint8Array
  ): Promise<string> {
    if (!this.db) {
      throw new Error('Database not initialized');
    }

    // Compute CID (SHA-256 hash)
    // Compute CID using ArrayBuffer copy
    const hashBuffer = await crypto.subtle.digest('SHA-256', new Uint8Array(data));
    const hashArray = new Uint8Array(hashBuffer);
    const cid = Array.from(hashArray)
      .map(b => b.toString(16).padStart(2, '0'))
      .join('');

    const record: StoredRecord = {
      cid,
      schema,
      peerId,
      timestamp: Date.now(),
      data,
      signature,
    };

    await this.db.put('records', record);
    return cid;
  }

  /**
   * Get a record by CID
   */
  async get(schema: SchemaName, cid: string): Promise<StoredRecord | null> {
    if (!this.db) {
      throw new Error('Database not initialized');
    }

    const record = await this.db.get('records', cid);
    if (record && record.schema === schema) {
      return record;
    }
    return null;
  }

  /**
   * Query records with optional filters
   */
  async query(schema: SchemaName, filter?: QueryFilter): Promise<StoredRecord[]> {
    if (!this.db) {
      throw new Error('Database not initialized');
    }

    let records: StoredRecord[];

    if (filter?.peerId) {
      // Query by schema and peer
      records = await this.db.getAllFromIndex(
        'records',
        'by-schema-peer',
        [schema, filter.peerId]
      );
    } else {
      // Query by schema
      records = await this.db.getAllFromIndex('records', 'by-schema', schema);
    }

    // Filter by time if needed
    if (filter?.since) {
      const sinceMs = filter.since.getTime();
      records = records.filter(r => r.timestamp >= sinceMs);
    }

    // Sort by timestamp descending
    records.sort((a, b) => b.timestamp - a.timestamp);

    // Apply limit
    if (filter?.limit && records.length > filter.limit) {
      records = records.slice(0, filter.limit);
    }

    return records;
  }

  /**
   * Delete a record by CID
   */
  async delete(cid: string): Promise<void> {
    if (!this.db) {
      throw new Error('Database not initialized');
    }

    await this.db.delete('records', cid);
  }

  /**
   * Get record count by schema
   */
  async count(schema: SchemaName): Promise<number> {
    if (!this.db) {
      throw new Error('Database not initialized');
    }

    return this.db.countFromIndex('records', 'by-schema', schema);
  }

  /**
   * Get statistics for all schemas
   */
  async stats(): Promise<Record<string, number>> {
    const result: Record<string, number> = {};

    for (const schema of SUPPORTED_SCHEMAS) {
      result[schema] = await this.count(schema);
    }

    return result;
  }

  /**
   * Clear old records
   */
  async garbageCollect(maxAgeMs: number): Promise<number> {
    if (!this.db) {
      throw new Error('Database not initialized');
    }

    const cutoff = Date.now() - maxAgeMs;
    const tx = this.db.transaction('records', 'readwrite');
    const index = tx.store.index('by-time');

    let deleted = 0;
    let cursor = await index.openCursor(IDBKeyRange.upperBound(cutoff));

    while (cursor) {
      await cursor.delete();
      deleted++;
      cursor = await cursor.continue();
    }

    return deleted;
  }

  /**
   * Close the database
   */
  async close(): Promise<void> {
    if (this.db) {
      this.db.close();
      this.db = null;
    }
  }

  /**
   * Delete the entire database
   */
  static async deleteDatabase(dbName: string = 'sdn-store'): Promise<void> {
    await indexedDB.deleteDatabase(dbName);
  }
}
