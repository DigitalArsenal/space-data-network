# Configuration Reference

Complete reference for all Space Data Network configuration options.

## Configuration File

The default configuration file is located at:
- **Full Node**: `~/.sdn/config.toml`
- **Edge Relay**: `~/.sdn-edge/config.toml`

Override with the `--config` flag:

```bash
spacedatanetwork daemon --config /etc/sdn/config.toml
```

## Environment Variables

All configuration options can be set via environment variables using the `SDN_` prefix:

```bash
export SDN_ADDRESSES_API="/ip4/0.0.0.0/tcp/5001"
export SDN_PUBSUB_ENABLED=true
export SDN_STORAGE_PATH="/data/sdn.db"
```

Environment variables override config file values.

## Complete Configuration

```toml
# Space Data Network Configuration
# Full reference with all options and defaults

# =============================================================================
# Identity
# =============================================================================

[identity]
# Peer ID - auto-generated on first run
# peer_id = "12D3KooWExample..."

# Private key (base64 encoded) - auto-generated on first run
# private_key = "CAESQLf..."

# Optional: Import existing key
# key_file = "/path/to/private.key"

# =============================================================================
# Network Addresses
# =============================================================================

[addresses]
# Swarm listening addresses
swarm = [
  "/ip4/0.0.0.0/tcp/4001",
  "/ip4/0.0.0.0/udp/4001/quic-v1",
  "/ip6/::/tcp/4001",
  "/ip6/::/udp/4001/quic-v1",
]

# API listening address (for CLI commands)
api = "/ip4/127.0.0.1/tcp/5001"

# WebSocket address (edge relay only)
# websocket = "/ip4/0.0.0.0/tcp/4002/ws"

# Announce addresses (for NAT traversal)
# Use when behind NAT or with port forwarding
announce = []
# Example:
# announce = [
#   "/ip4/203.0.113.1/tcp/4001",
#   "/dns4/mynode.example.com/tcp/4001",
# ]

# Addresses to avoid announcing
no_announce = []

# =============================================================================
# Bootstrap Peers
# =============================================================================

[bootstrap]
# Initial peers for network connectivity
peers = [
  "/dnsaddr/bootstrap.spacedatanetwork.org/p2p/12D3KooW...",
]

# Retry interval for failed bootstrap connections
retry_interval = "30s"

# Maximum bootstrap attempts before giving up
max_attempts = 10

# =============================================================================
# Discovery
# =============================================================================

[discovery]
# Enable mDNS for local network discovery
mdns = true
mdns_interval = "10s"

# DHT mode: "auto", "server", "client", "disabled"
# - auto: Server mode if publicly reachable, client otherwise
# - server: Always run as DHT server (requires public IP)
# - client: Only query DHT, don't serve requests
# - disabled: No DHT (not recommended)
dht_mode = "auto"

# DHT refresh interval
dht_refresh_interval = "10m"

# Enable hole punching for NAT traversal
hole_punching = true

# =============================================================================
# PubSub (GossipSub)
# =============================================================================

[pubsub]
# Enable PubSub messaging
enabled = true

# Topics to subscribe to (empty = all SDS schemas)
topics = []
# Example - subscribe only to specific schemas:
# topics = ["OMM", "CDM", "EPM"]

# GossipSub parameters
heartbeat_interval = "1s"
history_length = 5
history_gossip = 3

# Message validation
validate_signatures = true
strict_signing = true

# Flood publishing (send to all peers, not just mesh)
flood_publish = false

# =============================================================================
# Circuit Relay
# =============================================================================

[relay]
# Enable circuit relay
enabled = true

# Maximum relay hops (for browser connectivity)
hop_limit = 2

# Maximum concurrent relay reservations
max_reservations = 128

# Reservation duration
reservation_ttl = "1h"

# Data limit per reservation
data_limit = "128MB"

# =============================================================================
# Storage
# =============================================================================

[storage]
# Database path
path = "~/.sdn/sdn.db"

# Database engine: "sqlite", "badger"
engine = "sqlite"

# SQLite-specific options
[storage.sqlite]
# Journal mode: "wal", "delete", "truncate"
journal_mode = "wal"

# Synchronous mode: "off", "normal", "full", "extra"
synchronous = "normal"

# Cache size in KB
cache_size = 32768

# Garbage collection
[storage.gc]
enabled = true
interval = "1h"
max_age = "30d"

# Keep at least this many records per schema
min_records = 1000

# =============================================================================
# Resource Limits
# =============================================================================

[resource_limits]
# Maximum connections
max_conns = 500

# Maximum connections per peer
max_conns_per_peer = 8

# Maximum streams per connection
max_streams_per_conn = 256

# Memory limit
max_memory = "1GB"

# File descriptor limit
max_fds = 8192

# Connection timeouts
dial_timeout = "30s"
handshake_timeout = "30s"

# =============================================================================
# Rate Limiting
# =============================================================================

[rate_limit]
enabled = true

# Requests per second per peer
requests_per_second = 100

# Burst allowance
burst = 50

# Rate limit by IP (for edge relays)
by_ip = false

# =============================================================================
# TLS / Security
# =============================================================================

[tls]
# Certificate file (for WSS)
cert_file = ""

# Private key file
key_file = ""

# Auto-renew Let's Encrypt certificates
auto_cert = false
auto_cert_domain = ""
auto_cert_cache = "~/.sdn/certs"

# =============================================================================
# HTTP API
# =============================================================================

[api]
# Enable HTTP API
enabled = true

# Authentication
[api.auth]
enabled = false
api_key = ""

# CORS settings
[api.cors]
allowed_origins = ["*"]
allowed_methods = ["GET", "POST", "PUT", "DELETE"]
allowed_headers = ["Content-Type", "Authorization"]
max_age = 86400

# =============================================================================
# Metrics & Monitoring
# =============================================================================

[metrics]
# Enable Prometheus metrics
enabled = false

# Metrics endpoint address
address = "127.0.0.1:9090"

# Metrics path
path = "/metrics"

# Include libp2p metrics
libp2p_metrics = true

# Include storage metrics
storage_metrics = true

# =============================================================================
# Logging
# =============================================================================

[logging]
# Log level: "debug", "info", "warn", "error"
level = "info"

# Log format: "text", "json"
format = "text"

# Output: "stdout", "stderr", or file path
output = "stdout"

# Include caller information
caller = false

# Subsystem log levels
[logging.subsystems]
# libp2p = "warn"
# pubsub = "info"
# storage = "info"

# =============================================================================
# Data Ingestion
# =============================================================================

[ingestion]
# Enable ingestion pipeline
enabled = true

# Watch directories for new files
watch_dirs = []
# Example:
# watch_dirs = [
#   "/data/incoming/tle",
#   "/data/incoming/cdm",
# ]

# WASM converter plugins
[ingestion.converters]
# Path to WASM plugins
plugin_dir = "~/.sdn/plugins"

# Registered converters
# [[ingestion.converters.plugins]]
# name = "tle-to-omm"
# path = "tle-converter.wasm"
# input_type = "text/x-tle"
# output_schema = "OMM"

# =============================================================================
# Encryption at Rest
# =============================================================================

[encryption]
# Enable encryption for stored data
enabled = false

# Key derivation function: "argon2", "scrypt"
kdf = "argon2"

# Key file (or use password prompt)
key_file = ""

# Argon2 parameters
[encryption.argon2]
time = 3
memory = 65536
threads = 4

# =============================================================================
# Advanced
# =============================================================================

[advanced]
# Enable experimental features
experimental = false

# Profile CPU usage
cpu_profile = ""

# Profile memory usage
mem_profile = ""

# Enable pprof endpoint
pprof_enabled = false
pprof_address = "127.0.0.1:6060"
```

## Configuration Profiles

### Minimal (Embedded/Edge)

```toml
[resource_limits]
max_conns = 50
max_memory = "128MB"

[storage]
path = "/data/sdn.db"
[storage.gc]
max_age = "7d"

[relay]
max_reservations = 16

[pubsub]
history_length = 3
```

### High Performance

```toml
[resource_limits]
max_conns = 2000
max_streams_per_conn = 1024
max_memory = "8GB"

[storage]
path = "/ssd/sdn.db"
[storage.sqlite]
cache_size = 131072

[pubsub]
heartbeat_interval = "500ms"
flood_publish = true
```

### Privacy-Focused

```toml
[discovery]
mdns = false

[addresses]
no_announce = [
  "/ip4/10.0.0.0/8",
  "/ip4/172.16.0.0/12",
  "/ip4/192.168.0.0/16",
]

[logging]
level = "warn"

[metrics]
enabled = false
```

## Validation

Validate your configuration file:

```bash
spacedatanetwork config validate
```

Show effective configuration (including defaults):

```bash
spacedatanetwork config show
```

## Next Steps

- [Full Node Setup](/guide/full-node) - Deploy a full node
- [Edge Relay](/guide/edge-relay) - Set up browser connectivity
- [CLI Reference](/api/server) - Command documentation
