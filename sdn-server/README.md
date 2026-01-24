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
