# SDN Tasks

## Phase 9: Encrypted Traffic Testing

### 9.1 Cross-Node Encryption Tests

Create comprehensive test harnesses demonstrating encrypted traffic between all node types:

- [ ] **Browser-to-Server Encryption Test**
  - Browser (sdn-js) sends ECIES-encrypted FlatBuffer to Go server (sdn-server)
  - Verify X25519, secp256k1, and P-256 key exchange all work
  - Test with OMM, CDM, EPM message types

- [ ] **Server-to-Server Encryption Test**
  - Two sdn-server instances exchange encrypted messages
  - Test direct connection and relay-mediated connection
  - Verify PubSub encrypted message broadcast

- [ ] **Edge Relay Pass-Through Test**
  - Verify edge relays correctly forward encrypted traffic without decryption
  - Test circuit relay v2 with encrypted payloads
  - Measure latency overhead

- [ ] **Desktop-to-Desktop Test**
  - Electron app (sdn-desktop) encrypted communication
  - Test with large payload sizes (ephemeris data)

- [ ] **Mobile Wallet Browser Test**
  - Test Phantom/MetaMask in-app browser encrypted communication
  - Verify Web3 wallet key derivation for encryption

### 9.2 Test Infrastructure

- [ ] Create Docker Compose test network with multiple node types
- [ ] Add Playwright browser tests for sdn-js encryption
- [ ] Create Go test harness for server-to-server tests
- [ ] Add CI/CD pipeline for encryption regression tests

---

## Phase 10: Subscription UI and Routing

### 10.1 Data Type Subscription System

Design and implement UI for subscribing to specific SDS data types from different nodes:

- [ ] **Subscription Configuration Schema**
  ```typescript
  interface SubscriptionConfig {
    dataTypes: string[];        // e.g., ["OMM", "CDM", "EPM"]
    sourcePeers: string[];      // Peer IDs or "all"
    encrypted: boolean;         // Receive encrypted or plaintext
    streaming: boolean;         // Real-time vs batch
    filters?: QueryFilter[];    // Field-level filters
  }
  ```

- [ ] **Desktop App Subscription UI**
  - Data type selector (checkboxes for each SDS schema)
  - Source peer selector (trusted peers list)
  - Encryption toggle (encrypted/unencrypted stream)
  - Real-time streaming toggle
  - Filter builder for field-level queries

- [ ] **Server Admin Subscription UI**
  - Web-based configuration panel
  - Per-topic subscription management
  - Bandwidth/rate limiting controls
  - Logging and metrics dashboard

### 10.2 Header-Based Routing

Document and implement unencrypted header routing:

- [ ] **Routing Header Schema**
  ```flatbuffers
  table RoutingHeader {
    schema_type: string;      // "OMM", "CDM", etc. (unencrypted for routing)
    destination_peers: [string];
    ttl: uint8;
    priority: uint8;
    encrypted: bool;
  }
  ```

- [ ] **PubSub Topic Routing**
  - Route by schema type: `/sdn/data/{schema_type}`
  - Route by destination: `/sdn/peer/{peer_id}`
  - Implement topic-based filtering at edge relays

- [ ] **Streaming Modes**
  - Encrypted streaming (ECIES per-message or session key)
  - Unencrypted streaming (for public data like TLEs)
  - Hybrid mode (headers unencrypted, payload encrypted)

---

## Phase 11: XTCE to JSON Schema Converter (COMPLETE)

### 11.1 XTCE Parser

Create harness to read XTCE (XML Telemetry/Command Exchange) and convert to JSON Schema with x-flatbuffer annotations:

- [x] **XTCE Parser Module** (packages/sdn-xtce/src/parser.ts)
  - Parse XTCE XML format (CCSDS 660.1-G-2 standard)
  - Extract telemetry parameter definitions
  - Extract command definitions
  - Handle inheritance and reference resolution

- [x] **JSON Schema Generator** (packages/sdn-xtce/src/json-schema-generator.ts)
  - Convert XTCE types to JSON Schema types
  - Add `x-flatbuffer-type` annotations for FlatBuffer mapping
  - Add `x-flatbuffer-field-id` for stable serialization
  - Generate both `.json` schema and `.fbs` FlatBuffer schema

- [x] **Example Conversion**
  ```xml
  <!-- XTCE Input -->
  <TelemetryMetaData>
    <ParameterTypeSet>
      <IntegerParameterType name="Temperature" sizeInBits="16" signed="true"/>
    </ParameterTypeSet>
  </TelemetryMetaData>
  ```
  ```json
  // JSON Schema Output
  {
    "type": "object",
    "properties": {
      "Temperature": {
        "type": "integer",
        "x-flatbuffer-type": "int16",
        "x-flatbuffer-field-id": 1,
        "minimum": -32768,
        "maximum": 32767
      }
    }
  }
  ```

- [x] **CLI Tool** (packages/sdn-xtce/src/cli.ts)
  ```bash
  sdn-xtce convert --input spacecraft.xml --output-schema spacecraft.schema.json --output-fbs spacecraft.fbs
  ```

- [x] **API Endpoint** (sdn-server/internal/api/xtce.go)
  - Accept XTCE XML at `/api/ingest/xtce`
  - Auto-convert to FlatBuffer
  - Store in SQLite with generated schema
  - Broadcast encrypted via PubSub

### 11.2 Integration with flatbuffers/wasm

- [x] Use existing `x-flatbuffer` annotation patterns from `../flatbuffers/wasm`
- [x] Ensure generated schemas work with flatc compiler
- [x] Add validation tests with real XTCE samples (packages/sdn-xtce/test/converter.test.ts)

---

## Phase 12: Trusted Peer Registry

### 12.1 Peer Trust System

Implement trusted peer management (leverage IPFS Peering.Peers config):

- [ ] **Trust Levels**
  ```go
  type TrustLevel int
  const (
    Untrusted TrustLevel = iota  // No connection allowed
    Limited                       // Read-only, rate-limited
    Standard                      // Normal peer
    Trusted                       // Full access, priority routing
    Admin                         // Can manage other peers
  )
  ```

- [ ] **Trusted Peer Configuration**
  - Leverage existing IPFS `Peering.Peers` for always-connect peers
  - Add SDN-specific trust metadata
  - Support peer groups/organizations

- [ ] **Connection Policy**
  - Only connect to peers in trusted registry (optional strict mode)
  - Auto-reject connections from untrusted peers
  - Whitelist/blacklist management

### 12.2 Desktop Trusted Peer UI

- [ ] **Peer Management Panel**
  - List of known peers with trust levels
  - Add peer by Peer ID or multiaddr
  - Import peers from vCard/QR code (existing EPM→vCard support)
  - Peer groups for organization management

- [ ] **Visual Trust Indicators**
  - Green: Trusted/connected
  - Yellow: Known but not connected
  - Red: Blocked/untrusted
  - Connection quality metrics

- [ ] **Peer Discovery Controls**
  - Enable/disable DHT peer discovery
  - Enable/disable mDNS local discovery
  - Manual peer addition only mode (air-gapped)

### 12.3 Server Self-Hosted UI

- [ ] **Admin Web Interface**
  - Accessible at `http://localhost:5001/admin` (configurable port)
  - Peer trust management
  - Subscription configuration
  - System metrics and logs

- [ ] **Trusted Peer API**
  ```
  GET    /api/peers              # List all peers
  POST   /api/peers              # Add peer
  PUT    /api/peers/:id/trust    # Update trust level
  DELETE /api/peers/:id          # Remove peer
  GET    /api/peers/:id/stats    # Connection stats
  ```

---

## Phase 13: Server First-Time Setup Security

### 13.1 Initial Key Establishment

- [ ] **First-Run Detection**
  - Check for existing identity key at startup
  - If no key exists, enter setup mode

- [ ] **One-Time Setup Token**
  - Generate cryptographically random 32-character token on first start
  - Display token in terminal output:
    ```
    ══════════════════════════════════════════════════════════════
    FIRST-TIME SETUP - Space Data Network Server
    ══════════════════════════════════════════════════════════════

    Your one-time setup token (valid for 10 minutes):

        SETUP-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX

    Open http://localhost:5001/setup in your browser
    and enter this token to complete setup.

    WARNING: This token will only be shown once!
    ══════════════════════════════════════════════════════════════
    ```
  - Token expires after 10 minutes or first use
  - Store token hash (not plaintext) for verification

- [ ] **Setup Web Interface**
  - `/setup` route only accessible before initial setup
  - Token input form
  - After token verification:
    - Generate Ed25519 signing keypair
    - Generate X25519 encryption keypair
    - Create admin account with password/passkey
    - Generate EPM for server identity

### 13.2 Subsequent Login Security

- [ ] **Admin Authentication Options**
  - Password + TOTP (2FA)
  - WebAuthn/Passkey (recommended)
  - Hardware key (YubiKey)

- [ ] **Session Management**
  - JWT tokens with short expiry
  - Secure cookie with HttpOnly, SameSite=Strict
  - Session revocation on password change

- [ ] **Audit Logging**
  - Log all admin actions
  - Log peer trust changes
  - Log configuration changes
  - Tamper-evident log chain (hash-linked)

### 13.3 Key Backup and Recovery

- [ ] **Encrypted Key Export**
  - Export identity keys encrypted with user password
  - BIP-39 mnemonic backup option
  - QR code for mobile backup

- [ ] **Key Recovery**
  - Import from encrypted backup
  - Restore from mnemonic
  - Re-establish peer relationships after recovery

---

## Phase 14: Data Storefront / Marketplace

### 14.1 Data Listing Model

Define how data providers list their offerings:

- [ ] **Storefront Listing FlatBuffer Schema (STF.fbs)**
  ```flatbuffers
  table StorefrontListing {
    listing_id: string (required);       // Unique identifier
    provider_peer_id: string (required); // Seller's peer ID
    provider_epm_cid: string;            // Link to provider's EPM

    // Data description
    title: string (required);
    description: string;
    data_types: [string];                // ["OMM", "CDM", "TLE", etc.]
    coverage: DataCoverage;              // Spatial/temporal coverage
    sample_cid: string;                  // IPFS CID of sample data

    // Access model
    access_type: AccessType;             // OneTime, Subscription, Streaming
    encryption_required: bool;

    // Pricing
    pricing: [PricingTier];
    accepted_payments: [PaymentMethod];

    // Metadata
    created_at: uint64;                  // Unix timestamp
    updated_at: uint64;
    active: bool;

    // Signature
    signature: [ubyte];                  // Ed25519 signature from provider
  }

  enum AccessType : byte { OneTime, Subscription, Streaming, Query }

  table DataCoverage {
    spatial: SpatialCoverage;
    temporal: TemporalCoverage;
  }

  table SpatialCoverage {
    type: string;                        // "global", "region", "object_list"
    regions: [string];                   // e.g., ["LEO", "GEO", "MEO"]
    object_ids: [string];                // Specific NORAD IDs or catalog numbers
  }

  table TemporalCoverage {
    start_epoch: string;                 // ISO8601
    end_epoch: string;
    update_frequency: string;            // "realtime", "hourly", "daily"
    historical_depth: uint32;            // Days of historical data
  }

  table PricingTier {
    name: string;                        // "Basic", "Pro", "Enterprise"
    price_amount: uint64;                // In smallest unit (cents, satoshis, etc.)
    price_currency: string;              // "USD", "ETH", "SOL", "SDN_CREDITS"
    duration_days: uint32;               // 0 = one-time, >0 = subscription period
    rate_limit: uint32;                  // Requests per hour
    features: [string];
  }

  enum PaymentMethod : byte {
    Crypto_ETH,
    Crypto_SOL,
    Crypto_BTC,
    SDN_Credits,                         // Internal credits system
    Fiat_Stripe,
    Free                                 // For open data
  }
  ```

- [ ] **Access Control List (ACL) Schema**
  ```flatbuffers
  table DataAccessGrant {
    grant_id: string;
    listing_id: string;
    buyer_peer_id: string;
    buyer_encryption_pubkey: [ubyte];    // For encrypted delivery

    access_type: AccessType;
    tier_name: string;

    granted_at: uint64;
    expires_at: uint64;                  // 0 = never expires

    // Payment proof
    payment_tx_hash: string;             // On-chain tx or internal reference
    payment_method: PaymentMethod;

    // Provider signature
    provider_signature: [ubyte];
  }
  ```

### 14.2 Discovery and Search

- [ ] **DHT-Based Catalog**
  - Listings published to `/sdn/storefront/listings` PubSub topic
  - DHT key: `/sdn/listing/{listing_id}` → StorefrontListing FlatBuffer
  - Provider key: `/sdn/provider/{peer_id}/listings` → list of listing_ids
  - Category index: `/sdn/category/{data_type}` → list of listing_ids

- [ ] **Search API**
  ```typescript
  interface StorefrontQuery {
    dataTypes?: string[];           // Filter by data type
    priceMax?: number;              // Max price in USD equivalent
    accessTypes?: AccessType[];     // OneTime, Subscription, etc.
    spatialCoverage?: string[];     // Regions like "LEO", "GEO"
    objectIds?: string[];           // Specific satellite IDs
    providerPeerIds?: string[];     // Specific providers
    sortBy?: 'price' | 'rating' | 'updated' | 'relevance';
    limit?: number;
    offset?: number;
  }

  interface SearchResult {
    listings: StorefrontListing[];
    total: number;
    facets: {
      dataTypes: Record<string, number>;
      priceRanges: Record<string, number>;
      providers: Record<string, number>;
    };
  }
  ```

- [ ] **Indexer Service**
  - Subscribe to `/sdn/storefront/listings` for new listings
  - Index in SQLite for fast queries
  - Full-text search on title/description
  - Faceted filtering

### 14.3 Purchase and Access Flow

```
┌─────────────┐     1. Browse/Search      ┌─────────────────┐
│   Buyer     │ ─────────────────────────▶│   Storefront    │
│   (sdn-js)  │                           │   Index/DHT     │
└─────────────┘                           └─────────────────┘
       │                                          │
       │ 2. Select Listing                        │
       ▼                                          │
┌─────────────┐     3. Payment            ┌──────▼──────────┐
│   Payment   │ ◀────────────────────────│   Provider      │
│   Gateway   │                           │   (sdn-server)  │
└─────────────┘                           └─────────────────┘
       │                                          │
       │ 4. Payment Confirmed                     │
       ▼                                          │
┌─────────────┐     5. ACL Grant          ┌──────▼──────────┐
│   Buyer     │ ◀────────────────────────│   Provider      │
│             │                           │   Signs Grant   │
└─────────────┘                           └─────────────────┘
       │                                          │
       │ 6. Request Data (with ACL)               │
       ▼                                          ▼
┌─────────────┐     7. Encrypted Data     ┌─────────────────┐
│   Buyer     │ ◀────────────────────────│   Provider      │
│   Decrypts  │                           │   ECIES Encrypt │
└─────────────┘                           └─────────────────┘
```

- [ ] **Purchase Request (PUR.fbs)**
  ```flatbuffers
  table PurchaseRequest {
    request_id: string;
    listing_id: string;
    tier_name: string;

    buyer_peer_id: string;
    buyer_encryption_pubkey: [ubyte];

    payment_method: PaymentMethod;
    payment_amount: uint64;
    payment_currency: string;

    // For crypto payments
    payment_tx_hash: string;
    payment_chain: string;           // "ethereum", "solana", etc.

    // For credit/fiat
    payment_reference: string;

    buyer_signature: [ubyte];
    timestamp: uint64;
  }
  ```

- [ ] **Access Verification**
  - Provider receives data request with ACL grant attached
  - Verify grant signature matches provider's signing key
  - Verify buyer_peer_id matches requestor
  - Check expiration
  - Enforce rate limits

### 14.4 Payment Integration

- [ ] **Crypto Payments**
  - Accept ETH, SOL, BTC via wallet connect
  - Watch for on-chain payment confirmation
  - Auto-issue ACL grant on confirmation
  - Support for stablecoins (USDC, USDT)

- [ ] **SDN Credits System**
  - Internal credit balance per peer
  - Purchase credits via fiat or crypto
  - Instant settlement for data purchases
  - Credits stored in EPM or separate balance manifest

- [ ] **Fiat Gateway (Optional)**
  - Stripe integration for credit card payments
  - Auto-convert to SDN credits
  - Compliance with payment regulations

### 14.5 Data Delivery

- [ ] **Delivery Methods**
  ```typescript
  enum DeliveryMethod {
    PubSubStream,     // Real-time via PubSub topic
    DirectTransfer,   // libp2p stream to buyer
    IPFSPin,          // Pin data to IPFS, share CID
    WebhookPush,      // HTTP POST to buyer's endpoint
  }
  ```

- [ ] **Encrypted Delivery**
  - All paid data encrypted with buyer's public key (from ACL)
  - ECIES encryption using buyer's X25519/secp256k1 key
  - Session key for streaming (avoid per-message ECIES overhead)

- [ ] **Streaming Subscriptions**
  - Dedicated PubSub topic per subscription: `/sdn/data/{listing_id}/{buyer_peer_id}`
  - Provider publishes encrypted data to buyer's topic
  - Auto-renew or cancel on subscription expiry

### 14.6 Storefront UI

- [ ] **Seller Dashboard (Desktop/Web)**
  - Create and manage listings
  - View sales analytics
  - Manage ACL grants
  - Withdraw earnings
  - Set pricing tiers

- [ ] **Buyer Experience (Desktop/Web)**
  - Browse and search listings
  - View provider profiles (from EPM)
  - Purchase flow with wallet connect
  - Manage subscriptions
  - View purchased data

- [ ] **Listing Card Component**
  ```
  ┌────────────────────────────────────────────┐
  │ 🛰️ LEO Conjunction Data - Real-time        │
  │ Provider: SpaceCorp (★★★★☆ 4.2)           │
  ├────────────────────────────────────────────┤
  │ Data Types: CDM, TCA                       │
  │ Coverage: LEO (200-2000km)                 │
  │ Update: Real-time streaming                │
  ├────────────────────────────────────────────┤
  │ Basic: $49/mo  |  Pro: $199/mo            │
  │ [View Sample]  [Subscribe]                 │
  └────────────────────────────────────────────┘
  ```

### 14.7 Reputation and Trust

- [ ] **Provider Reputation**
  - Uptime tracking (data availability)
  - Delivery latency metrics
  - Buyer ratings and reviews
  - Data quality scores (schema compliance)

- [ ] **Review FlatBuffer (REV.fbs)**
  ```flatbuffers
  table Review {
    review_id: string;
    listing_id: string;
    reviewer_peer_id: string;

    rating: uint8;                   // 1-5 stars
    title: string;
    content: string;

    // Proof of purchase
    acl_grant_id: string;

    timestamp: uint64;
    reviewer_signature: [ubyte];
  }
  ```

- [ ] **Trust Scoring**
  - Combine peer trust level (Phase 12) with marketplace reputation
  - Higher trust = featured listings
  - Escrow for new/low-trust providers

---

## Phase 15: spacedatastandards.org Website & Schema Registry

### 15.1 Website Redesign

Mirror the space-data-network site design for spacedatastandards.org:

- [ ] **Site Structure**
  - Landing page with SDS overview
  - Schema catalog/registry as primary feature
  - Documentation section
  - Getting started guides
  - API reference

- [ ] **Technology Stack**
  - Same stack as space-data-network site (HTML/CSS/JS or framework used)
  - Dark theme with space aesthetic
  - Responsive design for mobile

- [ ] **Navigation**
  ```
  spacedatastandards.org/
  ├── /                     # Landing page
  ├── /schemas/             # Schema explorer (default view)
  ├── /schemas/{name}/      # Individual schema detail
  ├── /docs/                # Documentation
  ├── /docs/getting-started/
  ├── /docs/flatbuffers/
  ├── /docs/json-schema/
  ├── /api/                 # API reference
  └── /download/            # Bulk downloads
  ```

### 15.2 Schema Explorer

- [ ] **Schema Catalog View**
  - Grid/list view of all SDS schemas
  - Filter by category (Orbital, Conjunction, Entity, etc.)
  - Search by name or description
  - Show schema count badge on landing page

- [ ] **Individual Schema Page**
  ```
  ┌─────────────────────────────────────────────────────────────┐
  │ OMM - Orbit Mean-Elements Message                    v1.0.0 │
  │ Category: Orbital Data                                      │
  ├─────────────────────────────────────────────────────────────┤
  │ [JSON Schema] [FlatBuffers] [TypeScript] [Go] [Python]     │
  ├─────────────────────────────────────────────────────────────┤
  │ Description:                                                │
  │ The Orbit Mean-Elements Message contains orbital state...   │
  ├─────────────────────────────────────────────────────────────┤
  │ Schema Fields:                          [Expand All]        │
  │ ┌─────────────────────────────────────────────────────────┐ │
  │ │ ▼ OBJECT_NAME (string, required)                        │ │
  │ │   x-flatbuffer-type: string                             │ │
  │ │   x-flatbuffer-field-id: 1                              │ │
  │ │   Description: Spacecraft name                          │ │
  │ ├─────────────────────────────────────────────────────────┤ │
  │ │ ▶ EPOCH (string, required)                              │ │
  │ │ ▶ MEAN_MOTION (number, required)                        │ │
  │ │ ▶ ECCENTRICITY (number, required)                       │ │
  │ │ ...                                                     │ │
  │ └─────────────────────────────────────────────────────────┘ │
  ├─────────────────────────────────────────────────────────────┤
  │ Downloads:                                                  │
  │ [📄 JSON Schema] [📄 .fbs] [📄 TypeScript Types]           │
  │ [📦 Download All Formats (.zip)]                           │
  └─────────────────────────────────────────────────────────────┘
  ```

- [ ] **Interactive Field Explorer**
  - Expandable/collapsible field tree
  - Show `x-flatbuffer-type` and `x-flatbuffer-field-id` for each field
  - Highlight required vs optional fields
  - Show nested object relationships
  - Copy field path to clipboard

- [ ] **Schema Diff View**
  - Compare versions of same schema
  - Show added/removed/changed fields
  - Highlight breaking changes

### 15.3 JSON Schema as Primary Format

- [ ] **Default to JSON Schema**
  - JSON Schema with x-flatbuffer annotations is the canonical format
  - All other formats (FlatBuffers, TypeScript, Go) generated from JSON Schema
  - Display JSON Schema first in UI, other formats as tabs

- [ ] **x-flatbuffer Annotations Display**
  ```json
  {
    "type": "object",
    "properties": {
      "OBJECT_NAME": {
        "type": "string",
        "x-flatbuffer-type": "string",
        "x-flatbuffer-field-id": 1,
        "description": "Spacecraft name"
      },
      "EPOCH": {
        "type": "string",
        "format": "date-time",
        "x-flatbuffer-type": "string",
        "x-flatbuffer-field-id": 2
      }
    }
  }
  ```

- [ ] **Annotation Reference Docs**
  - Document all x-flatbuffer-* annotations
  - `x-flatbuffer-type`: Maps JSON type to FlatBuffer type
  - `x-flatbuffer-field-id`: Stable field ID for binary compatibility
  - `x-flatbuffer-deprecated`: Mark fields as deprecated
  - `x-flatbuffer-default`: Default value for field

### 15.4 Download Center

- [ ] **Individual Schema Downloads**
  - JSON Schema (.json)
  - FlatBuffers schema (.fbs)
  - TypeScript types (.d.ts)
  - Go structs (.go)
  - Python dataclasses (.py)
  - Rust structs (.rs)

- [ ] **Bulk Downloads**
  - All schemas in single format (.zip)
  - All schemas in all formats (.zip)
  - NPM package link (@spacedatastandards/schemas)
  - Go module link (github.com/...)

- [ ] **Version Selection**
  - Download specific schema version
  - Download latest stable
  - Download all versions

### 15.5 New Schema Types (Phase 14 Support)

Add new FlatBuffer schemas for storefront/marketplace:

- [ ] **STF.fbs - Storefront Listing**
  - Add to schemas/sds/ directory
  - Generate JSON Schema with x-flatbuffer annotations
  - Add to spacedatastandards.org catalog

- [ ] **PUR.fbs - Purchase Request**
  - Add to schemas/sds/ directory
  - Generate JSON Schema
  - Add to catalog

- [ ] **REV.fbs - Review**
  - Add to schemas/sds/ directory
  - Generate JSON Schema
  - Add to catalog

- [ ] **ACL.fbs - Access Control Grant**
  - Add to schemas/sds/ directory
  - Generate JSON Schema
  - Add to catalog

- [ ] **Schema Categories Update**
  ```
  Categories:
  ├── Orbital Data (OMM, OEM, OPM, TLE)
  ├── Conjunction (CDM, CAT, TCA)
  ├── Entity (EPM, PNM)
  ├── Telemetry (TDM, custom XTCE-derived)
  ├── Marketplace (STF, PUR, REV, ACL)  # NEW
  └── Routing (RHD - Routing Header)     # NEW
  ```

### 15.6 API Endpoints

- [ ] **Schema Registry API**
  ```
  GET  /api/schemas                    # List all schemas
  GET  /api/schemas/{name}             # Get schema metadata
  GET  /api/schemas/{name}/json-schema # Get JSON Schema
  GET  /api/schemas/{name}/flatbuffers # Get .fbs file
  GET  /api/schemas/{name}/typescript  # Get TypeScript types
  GET  /api/schemas/{name}/versions    # List all versions
  GET  /api/schemas/{name}@{version}   # Get specific version
  ```

- [ ] **Validation API**
  ```
  POST /api/validate
  Content-Type: application/json

  {
    "schema": "OMM",
    "data": { ... }
  }

  Response:
  {
    "valid": true,
    "errors": []
  }
  ```

- [ ] **Generation API**
  ```
  POST /api/generate
  Content-Type: application/json

  {
    "schema": "OMM",
    "format": "typescript"  // or "go", "python", "rust"
  }

  Response: Generated code
  ```

### 15.7 Integration with space-data-network

- [ ] **Cross-Site Links**
  - spacedatastandards.org links to SDN for implementation
  - SDN site links to spacedatastandards.org for schema reference
  - Shared design language/branding

- [ ] **Schema Sync**
  - schemas/sds/ directory is source of truth
  - Auto-deploy to spacedatastandards.org on schema changes
  - Version tags trigger new schema release

- [ ] **NPM Package Auto-Publish**
  - @spacedatastandards/schemas package
  - Contains JSON Schemas + TypeScript types
  - Auto-publish on schema version bump

---

## Phase 16: Infrastructure & Developer Experience (COMPLETE)

### 16.1 Root Package Scripts

- [x] **Root package.json** with unified npm scripts:
  - `npm run docs` / `docs:open` - Serve documentation site
  - `npm run test` / `test:go` / `test:js` - Run test suites
  - `npm run stress` / `stress:go` / `stress:js` / `stress:quick` - Run stress tests
  - `npm run server` / `server:edge` / `server:build` - Launch/build SDN servers
  - `npm run desktop` - Launch desktop app (with dependency checks)
  - `npm run webui` / `webui:build` - Develop/build local IPFS Web UI
  - `npm run install:all` / `install:js` / `install:desktop` / `install:webui` - Install dependencies
  - `npm run docker:up` / `docker:down` / `docker:logs` - Docker compose management

### 16.2 10GB Stress Tests (COMPLETE)

Isolated stress tests for high-volume FlatBuffer operations:

- [x] **Go Stress Tests** (sdn-server/internal/stress/)
  - `generator.go` - Parallel FlatBuffer batch generator (8 workers, 10K batch size)
  - `pinner.go` - File-backed CID tracker (solves memory issues with large IPFS listings)
  - `stress_test.go` - Tests: Generate10GB, PinAndTrack, IntegrityVerification, CIDDeterminism, TransferBetweenNodes
  - Build tag isolation: `//go:build stress` - not included in normal `go test ./...`
  - Verified: 10GB generated at 1,922 MB/s, 36.3M CIDs streamed at 4.8M CIDs/sec

- [x] **JS Stress Tests** (sdn-js/src/stress/)
  - `streaming.stress.test.ts` - Streaming FlatBuffer reception, backpressure, chunking, memory
  - Separate vitest config: `vitest.stress.config.ts` (4-hour timeout)
  - Excluded from normal test runs via `vitest.config.ts`

### 16.3 Local IPFS Web UI (COMPLETE)

- [x] **Clone ipfs-webui** into `webui/` directory for local customization
- [x] **Update desktop app** to serve from `webui/build/` instead of downloading from IPFS
- [x] **Dependency checks** in desktop script - shows helpful messages if deps/build missing

### 16.4 Documentation Site Updates (COMPLETE)

- [x] **Adversarial security flow diagram** with branching Monitor → Trusted/Compromised paths
- [x] **Third Party deposit value** visualization with dollar sign icon
- [x] **IPFS/libp2p hyperlinks** throughout docs (ipfs.tech, libp2p.io)
- [x] **spacedatastandards.org links** with simplified "CCSDS-compliant schemas" references
- [x] **IPFS section layout** - merged icon into description box
- [x] **FlatBuffers card fix** - removed nested anchor tag causing layout break
- [x] **FlatBuffers links** updated to digitalarsenal.github.io/flatbuffers/
- [x] **IPFS/libp2p center label** backdrop for readability

---

## Implementation Priority

| Phase | Priority | Estimated Effort | Dependencies |
|-------|----------|------------------|--------------|
| Phase 9: Encryption Tests | High | 2-3 days | Existing encryption code |
| Phase 10: Subscription UI | High | 3-4 days | Phase 9 tests passing |
| Phase 11: XTCE Converter | Medium | 2-3 days | None |
| Phase 12: Trusted Peers | High | 2-3 days | None |
| Phase 13: Server Setup | Critical | 2-3 days | Phase 12 |
| Phase 14: Data Storefront | High | 5-7 days | Phase 12, Phase 13 |
| Phase 15: spacedatastandards.org | High | 4-5 days | Phase 14 (new schemas) |

---

## Notes

- Headers remain unencrypted for routing efficiency (schema type, destination)
- Payload encryption uses ECIES (X25519/secp256k1/P-256 + AES-256-CTR + HMAC)
- XTCE support enables compatibility with existing spacecraft ground systems
- Trusted peer system builds on IPFS Peering.Peers but adds SDN-specific trust levels
- First-time setup token prevents unauthorized server takeover
- spacedatastandards.org is the canonical schema registry; JSON Schema with x-flatbuffer annotations is the primary format
- New marketplace schemas (STF, PUR, REV, ACL) must be added to both schemas/sds/ and spacedatastandards.org
