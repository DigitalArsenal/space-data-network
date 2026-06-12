# SDN Server

The SDN Server is the core Go implementation of Space Data Network, providing full node and edge relay functionality for decentralized space data exchange.

## Quick Start

```bash
# Build the server
go build -o spacedatanetwork ./cmd/spacedatanetwork

# Initialize configuration
./spacedatanetwork init

# Start the daemon
./spacedatanetwork daemon
```

## TOR by Default

`spacedatanetwork daemon` and `spacedatanetwork ingest` now start a local TOR
runtime by default and route outbound HTTP requests through TOR SOCKS5h.

For daemon mode, a deterministic v3 onion service is created from the node's
identity key material and published in server metadata:

- server metadata includes `onion_address`
- EPM `multiformat_address` includes the onion URL

Default config values:

```yaml
tor:
  enabled: true
  binary_path: tor
  socks_address: 127.0.0.1:9050
  start_timeout: 30s
  hidden_service_enabled: true
  hidden_service_port: 0      # auto: 80 or 443 based on admin TLS
  hidden_service_target: ""   # default: admin.listen_addr normalized to loopback
  bypass_local_addresses: true
```

Disable TOR explicitly (for local debugging only):

```yaml
tor:
  enabled: false
```

## Ingestion Workers (CelesTrak + Space-Track + UDL)

Run a one-time sync:

```bash
./spacedatanetwork ingest \
  --once \
  --storage-path /opt/data/sdn \
  --raw-path /opt/data/raw \
  --celestrak-catalog-url "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv" \
  --celestrak-satcat-url "https://celestrak.org/pub/satcat.txt"
```

Run continuous workers with Space-Track credentials:

```bash
export SPACETRACK_IDENTITY="your-identity"
export SPACETRACK_PASSWORD="your-password"
./spacedatanetwork ingest \
  --storage-path /opt/data/sdn \
  --raw-path /opt/data/raw \
  --celestrak-catalog-url "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv" \
  --celestrak-satcat-url "https://celestrak.org/pub/satcat.txt" \
  --celestrak-interval 3h \
  --satcat-interval 24h \
  --spacetrack-enabled true \
  --spacetrack-batch-days 3 \
  --spacetrack-batch-sleep 3s
```

Default source behavior:

- GP source: `https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv`
- SATCAT source: `https://celestrak.org/pub/satcat.txt` (fixed-width text)
- CelesTrak refresh minimum: 3 hours per endpoint (cached under `<raw-path>/cache`)
- Ingest stores both `OMM.fbs` and `MPE.fbs`; use `MPE` endpoints for orbit bulk consumers.

### Unified Data Library (UDL)

The `udl` source pulls the USSF Unified Data Library REST API
(`https://unifieddatalibrary.com`) with basic-auth credentials and the same
checkpointed, epoch-windowed batching used for Space-Track gap-fill:

```bash
export UDL_USERNAME="your-username"
export UDL_PASSWORD="your-password"
./spacedatanetwork ingest \
  --storage-path /opt/data/sdn \
  --raw-path /opt/data/raw \
  --udl-enabled true \
  --udl-start-day 2026-01-01 \
  --udl-batch-days 3 \
  --udl-batch-sleep 3s \
  --udl-poll-interval 30m \
  --udl-max-results 10000
```

Feeds and schema mappings:

- `/udl/elset` -> `OMM.fbs` (parser `udl-elset/v1`, source tags `udl / udl-elset`).
  If `MEAN_MOTION` is absent it is derived from `SEMI_MAJOR_AXIS`.
- `/udl/sgi` -> `SPW.fbs` (parser `udl-sgi/v1`, source tags `udl / udl-sgi`).
  `SGI_DATE -> DATE`, `F10 -> F107_OBS`, `F10B -> F107_OBS_CENTER81`,
  `AP -> AP_AVG`, `KP -> KP_SUM` (tenths).
- `/udl/conjunction` is not ingested: no CDM builder exists yet.

UDL behavior notes:

- Credentials: `--udl-username`/`--udl-password` flags or `UDL_USERNAME`/
  `UDL_PASSWORD` environment variables (same pattern as Space-Track). When
  credentials are missing the UDL worker logs a warning and skips.
- Incremental pulls are day-batched epoch windows (`epoch=start..end`,
  `sgiDate=start..end`) with `maxResults`/`firstResult` paging and a polite
  sleep between pages and batches.
- Checkpoints `udl_elset_last_day` and `udl_sgi_last_day` are persisted in
  `ingest-checkpoints.json`; raw page payloads are archived under
  `<raw-path>/udl/<day>/`.
- `CLASSIFICATION_MARKING` values cannot be written into the OMM
  `CLASSIFICATION_TYPE` field (the shared OMM builder exposes no
  classification setter), so per-batch marking counts are preserved in the
  ingest provenance JSON (`classification_markings`) under
  `<raw-path>/provenance/udl-elset/` and `<raw-path>/provenance/udl-sgi/`.
- Records missing a NORAD catalog ID or with malformed epochs are skipped and
  reported in provenance warnings instead of aborting the batch.

Production (systemd) credential location:

```bash
/etc/systemd/system/spacedatanetwork-ingest.service.d/spacetrack.conf
```

```ini
[Service]
Environment=SPACETRACK_IDENTITY=your-identity
Environment=SPACETRACK_PASSWORD=your-password
Environment=UDL_USERNAME=your-username
Environment=UDL_PASSWORD=your-password
```

Apply changes:

```bash
sudo systemctl daemon-reload
sudo systemctl restart spacedatanetwork-ingest
```

Legacy import from `/opt/data/satellite_data.db`:

```bash
./spacedatanetwork import-legacy-sqlite \
  --source-db /opt/data/satellite_data.db \
  --storage-path /opt/data/sdn \
  --batch-size 50000 \
  --provider-id space-data-network-02 \
  --source-name celestrak-gp-historical \
  --datastore-namespace
```

Resume behavior is checkpointed at:

```bash
/opt/data/sdn/datastores/<sdn-datastore-key>/legacy-import-checkpoint.json
```

`--datastore-namespace` writes historical `OMM.fbs` into an isolated
SDN-managed FlatSQL datastore keyed by schema, provider/source, producer peer
identity, batch head, query profile, snapshot, high-water marker, and artifact
hash. That keeps source identity at the datastore boundary instead of adding a
per-record source-tag row for the full historical archive. Omit the flag only
when you intentionally need the older mixed-store/source-tag import path. Live
CelesTrak CSV batches remain under `space-data-network-02 / celestrak-gp`.
Use `--batch-id`, `--source-url`, `--producer-peer-id`, and
`--producer-public-key` when replaying a specific archived source snapshot.

## Stripe Subscription Billing (Storefront)

The daemon now mounts storefront routes on the admin HTTP listener, including Stripe-backed checkout and webhook handling. The IPFS WebUI is mounted separately at `/webui`, while `/admin` remains reserved for admin/auth flows:

- `POST /api/storefront/purchases/{request_id}/pay-fiat`
- `POST /api/storefront/payments/stripe/webhook`

Set these environment variables on the server:

```bash
export STRIPE_SECRET_KEY="sk_live_..."
export STRIPE_WEBHOOK_SECRET="whsec_..."
export STRIPE_SUCCESS_URL="https://your-domain.example/billing/success?session_id={CHECKOUT_SESSION_ID}"
export STRIPE_CANCEL_URL="https://your-domain.example/billing/cancel"
```

If Stripe env vars are not set, fiat checkout falls back to the existing local stub behavior.

## Module Delivery And Capability Tokens

Full nodes still expose the public provider identity/discovery surface used by
the canonical SDN requester flow:

- Browser and node requesters discover providers by compressed secp256k1 public key via the DHT namespace `space-data-network/module-delivery/provider-pubkey`
- Full nodes publish `GET /api/module-delivery/provider` so requesters can
  learn the provider's compressed secp256k1 public key, peer ID, IPNS name,
  and relay addresses.
- Requesters fetch encrypted module bundles by CID over IPFS/libp2p and
  decrypt locally inside the unified wasm `licensing` module.

The public browser-facing legacy broker/bootstrap flows are not part of the
current SDN contract.

`sdn-server` now owns only the generic host/runtime side of that contract:

- generic module runtime loading through `modulert`
- provider descriptor/discovery support
- plugin catalog metadata, staged-artifact decrypt helpers, and signed plugin
  upload plumbing
- shared host ABI capabilities used by wasm modules

HTTP endpoints on the admin listener:

- `GET /api/module-delivery/provider` returns the public provider descriptor
- `POST /api/v1/plugins/upload` uploads signed plain WASM bundles into the provider catalog

Native TLS on admin/API listener (no reverse proxy):

```yaml
admin:
  enabled: true
  listen_addr: 0.0.0.0:443
  tls_enabled: true
  tls_cert_file: /etc/spacedatanetwork/tls/origin.crt
  tls_key_file: /etc/spacedatanetwork/tls/origin.key
  homepage_file: /opt/spacedatanetwork/web/index.html
```

With admin TLS enabled, the daemon also proxies incoming `Upgrade: websocket`
requests on the admin listener to the local libp2p WebSocket transport (for
example `:8080`). This enables browser clients to dial secure multiaddrs such as:

`/dns4/your-domain.example/tcp/443/wss/p2p/<peer-id>`

Set an admin token to enable entitlement updates:

```bash
export SDN_LICENSE_ADMIN_TOKEN="replace-with-random-secret"
```

Paid-scope example route:

- `GET /api/v1/data/secure/omm` (requires scope `api:data:read:premium`)

Data API response format:

- Default for `OMM`, `MPE`, `CAT` query endpoints: `application/x-flatbuffers`
- Stream framing: `uint32be-length-prefixed` records
- Raw FlatSQL data queries can stream record payloads directly with
  `Accept: application/vnd.sdn.flatbuffers.stream`
- JSON fallback for debugging: add `?format=json` (or `Accept: application/json`)

Bulk FlatBuffer endpoints (globe feed):

- `GET /api/v1/data/omm/bulk?day=YYYY-MM-DD&limit=50000` (FlatBuffers default)
- `GET /api/v1/data/mpe/bulk?day=YYYY-MM-DD&limit=50000` (FlatBuffers default)
- `GET /api/v1/data/cat/bulk?limit=50000` (FlatBuffers default)
- `GET /api/v1/data/spw/bulk?limit=50000` (FlatBuffers default)

Plugin catalog location:

- Default root: `${STORAGE_PATH}/license/plugins`
- Override with: `SDN_PLUGIN_ROOT`
- Catalog file: `${SDN_PLUGIN_ROOT}/catalog.json`

Example `catalog.json`:

```json
{
  "plugins": [
    {
      "id": "orbpro-core",
      "version": "2026.02.11",
      "required_scope": "orbpro:premium",
      "encrypted_path": "orbpro-core.wasm.enc",
      "key_path": "orbpro-core.key",
      "content_type": "application/wasm"
    }
  ]
}
```

For local OrbPro module-delivery seeding, write encrypted bundle bytes and
per-bundle content keys directly into the catalog root:

```bash
npm run seed:orbpro-module-catalog -- \
  --plugin-root /Users/tj/.orbpro/local-sdn/sdn-data/space-data-network-10080-13080-14080/data/dev/license/plugins \
  --with-conjunction
```

This helper creates `catalog.json` plus `<slug>.wasm.enc` and `<slug>.key`
files for the built-in OrbPro remote modules. Use it for the local
module-delivery provider path; `/api/v1/plugins/upload` is not sufficient for
the encrypted grant/CID flow because it stores `plain_path` catalog entries.

## Packages

### Core Packages

| Package | Description |
|---------|-------------|
| `internal/sds` | Space Data Standards schema builders and validators |
| `internal/vcard` | EPM to vCard/QR code conversion |
| `internal/pubsub` | PubSub topic management and PNM tip/queue system |
| `internal/storage` | FlatBuffer-aware SQLite storage |
| `internal/node` | libp2p node management |

---

## Space Data Standards (`internal/sds`)

Provides FlatBuffer builders for all Space Data Standards schemas with a fluent API pattern.

### Supported Schemas

| Schema | Description | Builder |
|--------|-------------|---------|
| OMM | Orbit Mean-Elements Message | `NewOMMBuilder()` |
| EPM | Entity Profile Message | `NewEPMBuilder()` |
| PNM | Publish Notification Message | `NewPNMBuilder()` |
| CAT | Catalog Entry | `NewCATBuilder()` |

### Usage

```go
import "github.com/spacedatanetwork/sdn-server/internal/sds"

// Create an OMM message
ommData := sds.NewOMMBuilder().
    WithObjectName("ISS (ZARYA)").
    WithObjectID("1998-067A").
    WithNoradCatID(25544).
    WithEpoch("2024-01-15T12:00:00.000Z").
    WithMeanMotion(15.49).
    WithEccentricity(0.0001215).
    WithInclination(51.6434).
    Build()

// Create an EPM message
epmData := sds.NewEPMBuilder().
    WithDN("John Doe").
    WithLegalName("Acme Corporation").
    WithEmail("john@acme.com").
    WithTelephone("+1-555-0100").
    WithAddress("123 Main St", "Springfield", "IL", "62701", "USA").
    WithKeys("signingKey123", "encryptionKey456").
    Build()

// Create a PNM message
pnmData := sds.NewPNMBuilder().
    WithCID("bafybeiabcdef1234567890").
    WithFileID("OMM").
    WithSignature("0xsignature123").
    Build()
```

### Performance

Benchmarks on Apple M3 Ultra:

| Operation | Time | Allocations |
|-----------|------|-------------|
| OMM Serialize | 327 ns | 1 alloc |
| OMM Deserialize | 5 ns | 0 allocs |
| EPM Serialize | 574 ns | 3 allocs |
| EPM Deserialize | 5 ns | 0 allocs |
| PNM Serialize | 207 ns | 1 alloc |
| PNM Deserialize | 5 ns | 0 allocs |

Zero-copy deserialization achieves **~250 million ops/sec**.

---

## vCard/QR Code (`internal/vcard`)

Provides bidirectional conversion between EPM (Entity Profile Message) FlatBuffers, vCard 4.0 format, and QR codes.

### EPM to vCard Field Mapping

| EPM Field | vCard Property |
|-----------|---------------|
| DN | FN (Formatted Name) |
| LEGAL_NAME | ORG (Organization) |
| FAMILY_NAME, GIVEN_NAME, etc. | N (Structured Name) |
| EMAIL | EMAIL |
| TELEPHONE | TEL |
| ADDRESS | ADR |
| JOB_TITLE | TITLE |
| OCCUPATION | ROLE |
| MULTIFORMAT_ADDRESS (IPNS) | URL |
| KEYS (Signing) | X-SIGNING-KEY |
| KEYS (Encryption) | X-ENCRYPTION-KEY |

### Usage

```go
import "github.com/spacedatanetwork/sdn-server/internal/vcard"

// EPM -> vCard
vcardStr, err := vcard.EPMToVCard(epmBytes)

// vCard -> EPM
epmBytes, err := vcard.VCardToEPM(vcardStr)

// EPM -> QR Code (PNG)
pngData, err := vcard.EPMToQR(epmBytes, 256) // 256x256 pixels

// QR Code -> EPM
epmBytes, err := vcard.QRToEPM(pngData)

// Direct vCard <-> QR
pngData, err := vcard.VCardToQR(vcardStr, 256)
vcardStr, err := vcard.QRToVCard(pngData)
```

### Full Roundtrip Example

```go
// Create EPM
builder := flatbuffers.NewBuilder(256)
// ... build EPM ...
epmBytes := builder.FinishedBytes()

// Convert to QR code
pngData, _ := vcard.EPMToQR(epmBytes, 512)

// Save QR to file
os.WriteFile("contact.png", pngData, 0644)

// Later, scan QR and recover EPM
scannedPNG, _ := os.ReadFile("contact.png")
recoveredEPM, _ := vcard.QRToEPM(scannedPNG)
```

---

## PNM Tip/Queue System (`internal/pubsub`)

The Tip/Queue system uses Publish Notification Messages (PNM) as the core messaging mechanism for content discovery and distribution. Instead of broadcasting all pinned data, nodes announce content availability via PNM, allowing subscribers to selectively fetch and pin content based on configurable policies.

### Architecture

```
Publisher                           Subscriber
    |                                   |
    |-- Pin content locally             |
    |-- Create PNM with CID + sig       |
    |-- Broadcast PNM on /sdn/PNM ------|--> Receive PNM
    |                                   |-- Check config for peer + schema
    |                                   |-- If autoFetch: fetch by CID
    |                                   |-- If autoPin: pin with TTL
```

### Configuration System

The system supports **per-source AND per-schema** configuration with priority-based resolution:

```go
import "github.com/spacedatanetwork/sdn-server/internal/pubsub"

config := pubsub.NewTipQueueConfig()

// Set system-wide defaults
config.DefaultAutoFetch = false
config.DefaultAutoPin = false
config.DefaultTTL = 24 * time.Hour
config.MaxQueueSize = 1000

// Set per-schema defaults
config.SetSchemaDefault("OMM", &pubsub.SchemaConfig{
    AutoFetch: true,  // Always fetch OMM data
    AutoPin:   true,  // Pin OMM data
    TTL:       12 * time.Hour,
    Priority:  5,     // Higher priority in queue
})

config.SetSchemaDefault("CDM", &pubsub.SchemaConfig{
    AutoFetch: true,  // Conjunction data is critical
    AutoPin:   true,
    TTL:       48 * time.Hour,
    Priority:  10,    // Highest priority
})

// Set per-source overrides
config.SetSourceOverride("trusted-partner-peer-id", &pubsub.SourceConfig{
    Trusted:   true,
    AutoFetch: pubsub.BoolPtr(true),  // Override for this peer
    AutoPin:   pubsub.BoolPtr(true),
    TTL:       pubsub.DurationPtr(72 * time.Hour),
})

// Set per-source per-schema override (highest priority)
config.SetSourceSchemaOverride("trusted-partner-peer-id", "OMM", &pubsub.SchemaConfig{
    AutoFetch: true,
    AutoPin:   true,
    TTL:       168 * time.Hour, // 1 week for trusted OMM data
    Priority:  10,
})
```

### Configuration Resolution Order

When a PNM is received, the configuration is resolved in this priority order:

1. **Source+Schema Override** (highest) - `SourceOverrides[peerID].SchemaOverrides[schema]`
2. **Source Override** - `SourceOverrides[peerID]`
3. **Schema Default** - `SchemaDefaults[schema]`
4. **System Default** (lowest) - `Default*` values

### TipQueue Usage

```go
// Create TipQueue with configuration
tq := pubsub.NewTipQueue(config)
tq.SetTopicManager(topicManager)
tq.SetFetcher(contentFetcher)  // Implements ContentFetcher interface
tq.SetPinner(contentPinner)    // Implements ContentPinner interface

// Register handler for received tips
tq.OnTip(func(tip *pubsub.Tip, config pubsub.ResolvedConfig) {
    log.Printf("Received tip from %s: CID=%s Schema=%s",
        tip.PeerID, tip.CID, tip.SchemaType)
    log.Printf("Config: AutoFetch=%v AutoPin=%v TTL=%v",
        config.AutoFetch, config.AutoPin, config.TTL)
})

// Start subscribing to PNM messages
err := tq.Subscribe()

// Publish a tip for content you've pinned
err = tq.PublishTip(ctx, pubsub.PublishOptions{
    CID:        "bafybeiabcdef1234567890",
    SchemaType: "OMM",
    FileName:   "iss-ephemeris.omm",
    Signature:  "0xsignature123",
})

// Query pending tips
ommTips := tq.GetTips("OMM")
allTips := tq.GetAllTips()
pinnedCIDs := tq.GetPinnedCIDs()

// Cleanup
tq.Close()
```

### Tip Structure

```go
type Tip struct {
    PeerID           string    // Source peer ID
    CID              string    // Content identifier
    SchemaType       string    // FILE_ID (e.g., "OMM", "CDM")
    FileName         string    // Optional filename
    MultiformatAddr  string    // Multiformat address
    Signature        string    // Digital signature
    PublishTimestamp time.Time // When published
    ReceivedAt       time.Time // When received
    Fetched          bool      // Whether content was fetched
    Pinned           bool      // Whether content was pinned
    PinExpiry        time.Time // When pin expires
}
```

### Interfaces

```go
// ContentFetcher fetches content by CID
type ContentFetcher interface {
    Fetch(ctx context.Context, cid string) ([]byte, error)
}

// ContentPinner pins and unpins content
type ContentPinner interface {
    Pin(ctx context.Context, cid string, ttl time.Duration) error
    Unpin(ctx context.Context, cid string) error
}
```

---

## Testing

```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./internal/sds/... ./internal/vcard/... ./internal/pubsub/...

# Run benchmarks
go test -bench=. -benchmem ./internal/sds/...

# Run specific test
go test -v -run TestEPMQRFullRoundtrip ./internal/vcard/...
```

### Test Coverage

| Package | Tests |
|---------|-------|
| `internal/sds` | 22 tests (roundtrip, builder, benchmark) |
| `internal/vcard` | 28 tests (conversion, QR, roundtrip) |
| `internal/pubsub` | 33 tests (config, tipqueue, concurrency) |

---

## Dependencies

Key dependencies:

| Package | Purpose |
|---------|---------|
| `github.com/google/flatbuffers` | FlatBuffer serialization |
| `github.com/emersion/go-vcard` | vCard 4.0 parsing/encoding |
| `github.com/skip2/go-qrcode` | QR code generation |
| `github.com/makiuchi-d/gozxing` | QR code scanning |
| `github.com/libp2p/go-libp2p-pubsub` | PubSub messaging |

---

## License

MIT License - see [LICENSE](../LICENSE) for details.
