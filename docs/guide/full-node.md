# Full Node Setup

This guide covers deploying and configuring a Space Data Network full node.

## Overview

A full node:
- Stores all received data locally
- Participates in DHT for peer discovery
- Relays PubSub messages
- Serves data requests from other nodes

## Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 1 core | 2+ cores |
| RAM | 512 MB | 2 GB |
| Storage | 1 GB | 10+ GB |
| Network | 1 Mbps | 10+ Mbps |

## Installation

### Download Binary

::: code-group

```bash [Linux x64]
curl -Lo spacedatanetwork \
  https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-linux-amd64
chmod +x spacedatanetwork
sudo mv spacedatanetwork /usr/local/bin/
```

```bash [macOS ARM64]
curl -Lo spacedatanetwork \
  https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-darwin-arm64
chmod +x spacedatanetwork
sudo mv spacedatanetwork /usr/local/bin/
```

:::

### Build from Source

```bash
git clone https://github.com/DigitalArsenal/go-space-data-network.git
cd go-space-data-network/sdn-server
go build -o spacedatanetwork ./cmd/spacedatanetwork
```

## Configuration

### Initialize

```bash
spacedatanetwork init
```

This creates `~/.sdn/config.toml` with default settings.

### Configuration File

```toml
# ~/.sdn/config.toml

[identity]
# Auto-generated on first run
# peer_id = "12D3KooW..."
# private_key = "..."

[addresses]
# Listen addresses
swarm = [
  "/ip4/0.0.0.0/tcp/4001",
  "/ip4/0.0.0.0/udp/4001/quic-v1",
  "/ip6/::/tcp/4001",
]

# API address (local only by default)
api = "/ip4/127.0.0.1/tcp/5001"

[bootstrap]
# Bootstrap peers for initial connectivity
peers = [
  "/dnsaddr/bootstrap.spacedatanetwork.org/p2p/12D3KooW...",
]

[pubsub]
# Enable GossipSub
enabled = true

# Schemas to subscribe to (empty = all)
topics = []

[relay]
# Enable circuit relay for browser clients
enabled = true
hop_limit = 2
max_reservations = 128

[storage]
# SQLite database path
path = "~/.sdn/sdn.db"

# Garbage collection
gc_interval = "1h"
max_age = "30d"

[discovery]
# mDNS for local network discovery
mdns = true
mdns_interval = "10s"

# DHT mode: "auto", "server", "client"
dht_mode = "auto"
```

### Key Configuration Options

#### Public Node

For a publicly accessible node:

```toml
[addresses]
swarm = [
  "/ip4/0.0.0.0/tcp/4001",
  "/ip4/0.0.0.0/udp/4001/quic-v1",
]

# Announce external address
announce = [
  "/ip4/YOUR_PUBLIC_IP/tcp/4001",
]

[relay]
enabled = true
```

#### High-Performance Node

For handling many connections:

```toml
[resource_limits]
max_conns = 1000
max_conns_per_peer = 10
max_streams_per_conn = 1000

[storage]
# Use SSD for better performance
path = "/ssd/sdn/sdn.db"
```

## Running

### Foreground

```bash
spacedatanetwork daemon
```

### Systemd Service

Create `/etc/systemd/system/spacedatanetwork.service`:

```ini
[Unit]
Description=Space Data Network Node
After=network.target

[Service]
Type=simple
User=sdn
Group=sdn
ExecStart=/usr/local/bin/spacedatanetwork daemon
Restart=always
RestartSec=5

# Security
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/sdn

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo useradd -r -s /bin/false sdn
sudo mkdir -p /var/lib/sdn
sudo chown sdn:sdn /var/lib/sdn

sudo systemctl daemon-reload
sudo systemctl enable spacedatanetwork
sudo systemctl start spacedatanetwork
```

### Docker

```bash
docker run -d \
  --name sdn-node \
  -p 4001:4001 \
  -p 4001:4001/udp \
  -p 5001:5001 \
  -v sdn-data:/data \
  ghcr.io/digitalarsenal/spacedatanetwork:latest
```

## Firewall

Open these ports:

| Port | Protocol | Purpose |
|------|----------|---------|
| 4001 | TCP | libp2p swarm |
| 4001 | UDP | QUIC transport |
| 5001 | TCP | HTTP API (local only) |

```bash
# UFW
sudo ufw allow 4001/tcp
sudo ufw allow 4001/udp

# firewalld
sudo firewall-cmd --permanent --add-port=4001/tcp
sudo firewall-cmd --permanent --add-port=4001/udp
sudo firewall-cmd --reload
```

## Monitoring

### Check Status

```bash
spacedatanetwork stats
```

### Prometheus Metrics

Enable metrics in config:

```toml
[metrics]
enabled = true
address = "127.0.0.1:9090"
```

Metrics endpoint: `http://localhost:9090/metrics`

### Log Files

```bash
# View logs
journalctl -u spacedatanetwork -f

# Debug logging
spacedatanetwork daemon --debug
```

## Maintenance

### Database Maintenance

```bash
# Trigger garbage collection
spacedatanetwork gc --max-age 7d

# Compact database
spacedatanetwork db compact

# Export data
spacedatanetwork db export --schema OMM --output omm-backup.json
```

### Updates

```bash
# Check current version
spacedatanetwork version

# Update (stop service first)
sudo systemctl stop spacedatanetwork
curl -Lo /tmp/spacedatanetwork https://github.com/...
sudo mv /tmp/spacedatanetwork /usr/local/bin/
sudo systemctl start spacedatanetwork
```

## Troubleshooting

### Node Not Connecting

1. Check firewall rules
2. Verify bootstrap peers are reachable
3. Check for NAT issues

```bash
# Test connectivity
spacedatanetwork swarm connect /ip4/1.2.3.4/tcp/4001/p2p/12D3KooW...
```

### High Memory Usage

Reduce connection limits:

```toml
[resource_limits]
max_conns = 200
```

### Database Growing Large

Adjust garbage collection:

```toml
[storage]
gc_interval = "6h"
max_age = "7d"
```

## Next Steps

- [Edge Relay Setup](/guide/edge-relay) - Enable browser connectivity
- [Configuration Reference](/guide/configuration) - All config options
- [CLI Reference](/api/server) - Command documentation
