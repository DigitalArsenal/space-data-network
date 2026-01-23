# Server CLI Reference

Complete command-line reference for the Space Data Network server.

## Commands

### spacedatanetwork

The main SDN full node binary.

```bash
spacedatanetwork [command] [flags]
```

#### Global Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--config` | Path to config file | `~/.sdn/config.toml` |
| `--data-dir` | Data directory | `~/.sdn` |
| `--debug` | Enable debug logging | `false` |
| `-h, --help` | Show help | - |

### init

Initialize a new SDN node configuration.

```bash
spacedatanetwork init [flags]
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--profile` | Config profile: `server`, `edge`, `desktop` | `server` |
| `--key` | Path to existing private key | - |
| `--force` | Overwrite existing config | `false` |

**Example:**

```bash
# Initialize with default settings
spacedatanetwork init

# Initialize as edge relay
spacedatanetwork init --profile edge

# Use existing key
spacedatanetwork init --key /path/to/private.key
```

### daemon

Start the SDN daemon.

```bash
spacedatanetwork daemon [flags]
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--api-port` | HTTP API port | `5001` |
| `--swarm-port` | libp2p swarm port | `4001` |
| `--bootstrap` | Bootstrap peer addresses | Built-in list |
| `--enable-relay` | Enable circuit relay | `true` |
| `--enable-pubsub` | Enable GossipSub | `true` |
| `--db-path` | SQLite database path | `~/.sdn/sdn.db` |
| `--wasm-path` | Path to flatc WASM | Built-in |

**Example:**

```bash
# Start with defaults
spacedatanetwork daemon

# Custom ports
spacedatanetwork daemon --api-port 8080 --swarm-port 4002

# Add bootstrap peers
spacedatanetwork daemon --bootstrap /ip4/1.2.3.4/tcp/4001/p2p/12D3KooW...
```

### version

Print version information.

```bash
spacedatanetwork version
```

**Output:**

```
spacedatanetwork v1.0.0
  Commit: abc1234
  Built: 2024-01-15T12:00:00Z
  Go: go1.21.5
```

### id

Show node identity information.

```bash
spacedatanetwork id [flags]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--format` | Output format: `text`, `json` |
| `--addrs` | Include listen addresses |
| `--pubkey` | Include public key |

**Example:**

```bash
spacedatanetwork id --format json
```

```json
{
  "ID": "12D3KooWExample...",
  "PublicKey": "CAESIExample...",
  "Addresses": [
    "/ip4/0.0.0.0/tcp/4001",
    "/ip4/0.0.0.0/udp/4001/quic-v1"
  ]
}
```

### peers

List connected peers.

```bash
spacedatanetwork peers [command]
```

**Subcommands:**

```bash
# List all peers
spacedatanetwork peers list

# Show peer details
spacedatanetwork peers show <peer-id>

# Connect to peer
spacedatanetwork peers connect <multiaddr>

# Disconnect peer
spacedatanetwork peers disconnect <peer-id>
```

### publish

Publish data to the network.

```bash
spacedatanetwork publish <schema> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `schema` | Schema name (e.g., `OMM`, `CDM`) |

**Flags:**

| Flag | Description |
|------|-------------|
| `--file` | Read data from file |
| `--stdin` | Read data from stdin |
| `--json` | Input is JSON (convert to FlatBuffer) |

**Example:**

```bash
# Publish from file
spacedatanetwork publish OMM --file iss.omm

# Publish JSON (converted automatically)
echo '{"OBJECT_NAME":"ISS",...}' | spacedatanetwork publish OMM --stdin --json
```

### subscribe

Subscribe to data streams.

```bash
spacedatanetwork subscribe <schema> [flags]
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--output` | Output format: `json`, `binary`, `pretty` | `pretty` |
| `--filter` | Filter expression | - |
| `--limit` | Max messages to receive | 0 (unlimited) |

**Example:**

```bash
# Subscribe to all OMM messages
spacedatanetwork subscribe OMM

# Filter by object
spacedatanetwork subscribe OMM --filter 'NORAD_CAT_ID == 25544'

# Output as JSON
spacedatanetwork subscribe CDM --output json
```

### query

Query stored data.

```bash
spacedatanetwork query <schema> [expression] [flags]
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--limit` | Max results | 100 |
| `--offset` | Skip first N results | 0 |
| `--order` | Sort order: `asc`, `desc` | `desc` |
| `--output` | Output format | `pretty` |

**Example:**

```bash
# Query all OMM records
spacedatanetwork query OMM

# Query with filter
spacedatanetwork query OMM 'INCLINATION > 50'

# Limit results
spacedatanetwork query CDM --limit 10 --order asc
```

### ingest

Ingest and convert data.

```bash
spacedatanetwork ingest [flags]
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--input` | Input file or directory |
| `--format` | Input format (e.g., `tle`, `xml`) |
| `--schema` | Target schema |
| `--output` | Output file (or directory for batch) |
| `--publish` | Publish after conversion |

**Example:**

```bash
# Convert TLE to OMM
spacedatanetwork ingest --input iss.tle --format tle --schema OMM --output iss.omm

# Batch convert and publish
spacedatanetwork ingest --input ./tle-files/ --format tle --publish
```

### stats

Show node statistics.

```bash
spacedatanetwork stats [flags]
```

**Output:**

```
SDN Node Statistics
===================

Peer ID: 12D3KooWExample...
Uptime: 3h 24m 15s

Network:
  Connected Peers: 47
  DHT Peers: 1,234
  Relay Connections: 12

Storage:
  Total Records: 15,678
  Database Size: 245 MB

  By Schema:
    OMM: 12,345 records
    CDM: 2,456 records
    EPM: 567 records
    ...

PubSub:
  Active Topics: 8
  Messages/min: 234
```

---

## spacedatanetwork-edge

Lightweight edge relay binary.

```bash
spacedatanetwork-edge [flags]
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--port` | Relay port | `4001` |
| `--ws-port` | WebSocket port | `4002` |
| `--bootstrap` | Bootstrap peers | Built-in |
| `--max-connections` | Max concurrent connections | 100 |
| `--metrics` | Enable Prometheus metrics | `false` |
| `--metrics-port` | Metrics port | `9090` |

**Example:**

```bash
# Start edge relay
spacedatanetwork-edge

# With WebSocket on custom port
spacedatanetwork-edge --ws-port 8080

# Enable metrics
spacedatanetwork-edge --metrics --metrics-port 9090
```

---

## registry-builder

DHT monitor for edge relay discovery.

```bash
registry-builder [flags]
```

**Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `-b, --bootstrap` | Bootstrap peer addresses | - |
| `-s, --build-script` | Path to build script | `./scripts/build-edge-registry.ts` |
| `-o, --output` | Output directory | `./sdn-js/wasm` |
| `-c, --cdn` | CDN endpoints for deployment | - |
| `-p, --poll` | DHT poll interval | `5m` |
| `-t, --stale` | Stale relay timeout | `1h` |
| `-d, --debug` | Enable debug logging | `false` |

**Example:**

```bash
# Start registry builder
registry-builder --bootstrap /ip4/1.2.3.4/tcp/4001/p2p/12D3KooW...

# Deploy to CDN
registry-builder --cdn s3://my-bucket/wasm --poll 10m
```

---

## Configuration File

`~/.sdn/config.toml`:

```toml
[identity]
peer_id = "12D3KooW..."
private_key = "CAESQExample..."

[addresses]
swarm = [
  "/ip4/0.0.0.0/tcp/4001",
  "/ip4/0.0.0.0/udp/4001/quic-v1",
  "/ip6/::/tcp/4001",
]
api = "/ip4/127.0.0.1/tcp/5001"

[bootstrap]
peers = [
  "/dnsaddr/bootstrap.spacedatanetwork.org/p2p/12D3KooW...",
]

[pubsub]
enabled = true
topics = ["OMM", "CDM", "EPM"]

[relay]
enabled = true
hop_limit = 2

[storage]
path = "~/.sdn/sdn.db"
gc_interval = "1h"
max_age = "30d"

[wasm]
flatc_path = ""  # Use built-in
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `SDN_CONFIG` | Config file path |
| `SDN_DATA_DIR` | Data directory |
| `SDN_LOG_LEVEL` | Log level: debug, info, warn, error |
| `SDN_BOOTSTRAP` | Bootstrap peers (comma-separated) |
