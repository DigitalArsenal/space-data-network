# SDS Exchange Protocol

The Space Data Standards Exchange Protocol defines how SDN nodes communicate and share space data.

## Overview

The SDS Exchange Protocol is a libp2p-based protocol for:

- Publishing and subscribing to space data
- Querying historical data from peers
- Verifying data authenticity
- Managing data synchronization

## Protocol Identifier

```
/spacedatanetwork/sds-exchange/1.0.0
```

## Message Types

### PUBLISH

Broadcast new data to the network.

```flatbuffers
table SDSPublish {
  // Message metadata
  message_id: string;              // Unique message ID
  timestamp: string;               // ISO 8601 timestamp
  ttl: uint32;                     // Time-to-live (seconds)

  // Data
  schema: string;                  // Schema type (OMM, CDM, etc.)
  payload: [uint8];                // FlatBuffers-encoded data

  // Authentication
  publisher_id: string;            // Publisher Peer ID
  signature: [uint8];              // Ed25519 signature
}
```

### SUBSCRIBE

Request to receive specific data types.

```flatbuffers
table SDSSubscribe {
  // Subscription
  subscription_id: string;         // Unique subscription ID
  schemas: [string];               // Schemas to subscribe to

  // Filters (optional)
  filter: SDSFilter;               // Data filter criteria

  // Options
  include_historical: bool;        // Include recent historical data
  historical_limit: uint32;        // Max historical records
}

table SDSFilter {
  field: string;                   // Field to filter on
  operator: string;                // eq, gt, lt, contains, etc.
  value: string;                   // Filter value
  and_filters: [SDSFilter];        // AND conditions
  or_filters: [SDSFilter];         // OR conditions
}
```

### QUERY

Request specific data from a peer.

```flatbuffers
table SDSQuery {
  // Query identification
  query_id: string;                // Unique query ID

  // Query parameters
  schema: string;                  // Schema to query
  filter: SDSFilter;               // Filter criteria
  order_by: string;                // Sort field
  order: string;                   // asc or desc
  limit: uint32;                   // Maximum results
  offset: uint32;                  // Pagination offset

  // Options
  include_signatures: bool;        // Include message signatures
}
```

### QUERY_RESPONSE

Response to a query request.

```flatbuffers
table SDSQueryResponse {
  // Response metadata
  query_id: string;                // Original query ID
  total_count: uint32;             // Total matching records
  returned_count: uint32;          // Records in this response

  // Results
  records: [SDSRecord];            // Matching records

  // Pagination
  has_more: bool;                  // More results available
  next_offset: uint32;             // Offset for next page
}

table SDSRecord {
  schema: string;                  // Record schema
  data: [uint8];                   // FlatBuffers payload
  received_at: string;             // When record was received
  publisher_id: string;            // Original publisher
  signature: [uint8];              // Original signature
}
```

### SYNC

Request data synchronization.

```flatbuffers
table SDSSync {
  // Sync parameters
  sync_id: string;                 // Unique sync ID
  schemas: [string];               // Schemas to sync
  since: string;                   // Sync from timestamp

  // Bloom filter for deduplication
  bloom_filter: [uint8];           // Records already held
  bloom_hash_count: uint8;         // Number of hash functions
}
```

## Message Flow

### Publishing Data

```
Publisher                          Network
    |                                 |
    |  PUBLISH(OMM data)             |
    |-------------------------------->|
    |                                 |
    |  (GossipSub propagation)        |
    |                                 |
    |                         Subscriber
    |                                 |
    |                   (receive data)|
    |                                 |
```

### Querying Data

```
Client                             Peer
    |                                 |
    |  QUERY(filter, limit)          |
    |-------------------------------->|
    |                                 |
    |       QUERY_RESPONSE(records)  |
    |<--------------------------------|
    |                                 |
```

### Synchronization

```
Node A                           Node B
    |                                 |
    |  SYNC(schemas, since, bloom)   |
    |-------------------------------->|
    |                                 |
    |      (filter known records)     |
    |                                 |
    |       SYNC_RESPONSE(new data)  |
    |<--------------------------------|
    |                                 |
```

## GossipSub Topics

Data is published to schema-specific GossipSub topics:

```
/spacedatanetwork/{network_id}/{schema}
```

Examples:
- `/spacedatanetwork/mainnet/OMM`
- `/spacedatanetwork/mainnet/CDM`
- `/spacedatanetwork/testnet/EPM`

## Authentication

### Message Signing

All published messages are signed using Ed25519:

```typescript
// Signing process
const message = {
  schema: 'OMM',
  payload: ommBytes,
  timestamp: new Date().toISOString(),
  publisher_id: node.peerId
};

const messageBytes = serialize(message);
const signature = ed25519.sign(messageBytes, privateKey);
```

### Signature Verification

```typescript
// Verification process
const messageBytes = serialize(message);
const publicKey = await getPublicKey(message.publisher_id);
const isValid = ed25519.verify(signature, messageBytes, publicKey);
```

## Rate Limiting

Nodes implement rate limiting to prevent abuse:

| Limit | Default | Description |
|-------|---------|-------------|
| Publish rate | 10/s | Max publishes per second |
| Query rate | 100/s | Max queries per second |
| Message size | 1 MB | Maximum message size |
| Subscription limit | 100 | Max active subscriptions |

## Error Handling

### Error Codes

| Code | Name | Description |
|------|------|-------------|
| 1 | INVALID_MESSAGE | Malformed message |
| 2 | INVALID_SIGNATURE | Signature verification failed |
| 3 | RATE_LIMITED | Rate limit exceeded |
| 4 | SCHEMA_UNKNOWN | Unknown schema type |
| 5 | QUERY_TIMEOUT | Query timed out |
| 6 | PEER_UNAVAILABLE | Peer not reachable |

### Error Response

```flatbuffers
table SDSError {
  code: uint16;                    // Error code
  message: string;                 // Human-readable message
  request_id: string;              // Original request ID
  retry_after: uint32;             // Seconds before retry (rate limit)
}
```

## Implementation

### JavaScript

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode();
await node.start();

// Publishing uses SDS Exchange internally
await node.publish('OMM', ommData);

// Subscribing
node.subscribe('OMM', (data, peerId) => {
  // Message verified and deserialized by SDK
});

// Querying
const results = await node.query('OMM', {
  where: { NORAD_CAT_ID: 25544 },
  limit: 10
});
```

### Go

```go
import (
    "github.com/spacedatanetwork/sdn-server/internal/protocol"
)

// Create exchange handler
exchange := protocol.NewSDSExchange(host, datastore)

// Publish
msg := &protocol.SDSPublish{
    Schema:  "OMM",
    Payload: ommBytes,
}
exchange.Publish(ctx, msg)

// Subscribe
exchange.Subscribe("OMM", func(msg *protocol.SDSMessage) {
    // Handle message
})

// Query
results, err := exchange.Query(ctx, peerId, &protocol.SDSQuery{
    Schema: "OMM",
    Limit:  10,
})
```

## Wire Format

Messages are encoded as FlatBuffers with a 4-byte length prefix:

```
+-------------------+----------------------+
| Length (4 bytes)  | FlatBuffers Payload  |
+-------------------+----------------------+
```

## Compatibility

The protocol includes version negotiation:

```flatbuffers
table SDSHello {
  protocol_version: string;        // "1.0.0"
  supported_schemas: [string];     // Supported schema types
  capabilities: [string];          // Optional capabilities
}
```

## See Also

- [ID Exchange Protocol](/reference/protocol-id)
- [PubSub Topics](/reference/pubsub)
- [Schema Reference](/reference/schemas)
