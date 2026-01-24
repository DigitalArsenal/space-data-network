# Edge Relay Setup

Edge relays are lightweight SDN nodes designed to bridge browser clients to the full peer-to-peer network.

## Overview

An edge relay:
- Provides WebSocket/WebRTC endpoints for browsers
- Proxies PubSub messages between browsers and full nodes
- Requires minimal resources
- Can run on embedded devices

## Why Edge Relays?

Browsers cannot directly participate in libp2p networks due to security restrictions. Edge relays solve this by:

1. **WebSocket Bridge** - Translating browser WebSocket connections to libp2p protocols
2. **Circuit Relay** - Acting as a hop point for browser-to-browser communication
3. **Protocol Translation** - Converting between HTTP and libp2p streams

## Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 1 core | 1+ cores |
| RAM | 128 MB | 256 MB |
| Storage | 100 MB | 500 MB |
| Network | 1 Mbps | 10+ Mbps |

## Installation

### Download Binary

::: code-group

```bash [Linux x64]
curl -Lo spacedatanetwork-edge \
  https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-edge-linux-amd64
chmod +x spacedatanetwork-edge
sudo mv spacedatanetwork-edge /usr/local/bin/
```

```bash [Linux ARM64]
curl -Lo spacedatanetwork-edge \
  https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-edge-linux-arm64
chmod +x spacedatanetwork-edge
sudo mv spacedatanetwork-edge /usr/local/bin/
```

```bash [macOS ARM64]
curl -Lo spacedatanetwork-edge \
  https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-edge-darwin-arm64
chmod +x spacedatanetwork-edge
sudo mv spacedatanetwork-edge /usr/local/bin/
```

:::

### Build from Source

```bash
git clone https://github.com/DigitalArsenal/go-space-data-network.git
cd go-space-data-network/sdn-server
go build -tags edge -o spacedatanetwork-edge ./cmd/spacedatanetwork-edge
```

## Configuration

### Initialize

```bash
spacedatanetwork-edge init
```

Creates `~/.sdn-edge/config.toml` with default settings.

### Configuration File

```toml
# ~/.sdn-edge/config.toml

[identity]
# Auto-generated on first run
# peer_id = "12D3KooW..."

[addresses]
# WebSocket endpoint for browsers
websocket = "/ip4/0.0.0.0/tcp/4002/ws"

# Secure WebSocket (recommended for production)
# websocket_tls = "/ip4/0.0.0.0/tcp/4003/wss"

# libp2p swarm for connecting to full nodes
swarm = [
  "/ip4/0.0.0.0/tcp/4001",
  "/ip4/0.0.0.0/udp/4001/quic-v1",
]

[tls]
# For WSS (WebSocket Secure)
cert_file = "/etc/sdn/cert.pem"
key_file = "/etc/sdn/key.pem"

[bootstrap]
# Connect to the main network
peers = [
  "/dnsaddr/bootstrap.spacedatanetwork.org/p2p/12D3KooW...",
]

[relay]
# Enable circuit relay for browser-to-browser
enabled = true
hop_limit = 1
max_reservations = 64

[pubsub]
enabled = true
# Relay all topics
topics = []

[cors]
# Allow browser connections
allowed_origins = ["*"]
# Or restrict to specific domains:
# allowed_origins = ["https://yourdomain.com"]
```

### Security Considerations

For production deployments:

```toml
[cors]
# Restrict to your domains
allowed_origins = [
  "https://app.spacedatanetwork.org",
  "https://yourdomain.com",
]

[rate_limit]
enabled = true
requests_per_second = 100
burst = 50

[auth]
# Optional: Require authentication
enabled = false
# api_key = "your-secret-key"
```

## Running

### Foreground

```bash
spacedatanetwork-edge
```

### With TLS (Recommended)

```bash
spacedatanetwork-edge --tls-cert /etc/sdn/cert.pem --tls-key /etc/sdn/key.pem
```

### Docker

```bash
docker run -d \
  --name sdn-edge \
  -p 4001:4001 \
  -p 4001:4001/udp \
  -p 4002:4002 \
  -v sdn-edge-data:/data \
  ghcr.io/digitalarsenal/spacedatanetwork-edge:latest
```

### Docker Compose

```yaml
version: '3.8'
services:
  sdn-edge:
    image: ghcr.io/digitalarsenal/spacedatanetwork-edge:latest
    ports:
      - "4001:4001"
      - "4001:4001/udp"
      - "4002:4002"
    volumes:
      - sdn-edge-data:/data
    restart: unless-stopped

volumes:
  sdn-edge-data:
```

## Connecting from Browsers

Once your edge relay is running, browsers can connect:

```typescript
import { SDNNode } from '@spacedatanetwork/sdn-js';

const node = new SDNNode({
  relays: [
    '/ip4/your-edge-relay.com/tcp/4002/ws/p2p/12D3KooW...'
  ]
});

await node.start();
```

For secure WebSocket:

```typescript
const node = new SDNNode({
  relays: [
    '/dns4/your-edge-relay.com/tcp/443/wss/p2p/12D3KooW...'
  ]
});
```

## Load Balancing

For high-availability deployments, run multiple edge relays behind a load balancer:

```nginx
upstream sdn_edges {
    server edge1.example.com:4002;
    server edge2.example.com:4002;
    server edge3.example.com:4002;
}

server {
    listen 443 ssl;
    server_name relay.spacedatanetwork.org;

    ssl_certificate /etc/ssl/certs/relay.crt;
    ssl_certificate_key /etc/ssl/private/relay.key;

    location / {
        proxy_pass http://sdn_edges;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

## Monitoring

### Health Check

```bash
curl http://localhost:4002/health
# {"status":"ok","peers":5,"uptime":"2h35m"}
```

### Metrics

Enable Prometheus metrics:

```toml
[metrics]
enabled = true
address = "127.0.0.1:9091"
```

Key metrics:
- `sdn_edge_connections_active` - Current browser connections
- `sdn_edge_messages_relayed_total` - Total messages proxied
- `sdn_edge_bytes_transferred_total` - Bandwidth usage

## Embedded Deployment

Edge relays are designed for resource-constrained environments:

### Raspberry Pi

```bash
# ARM64
curl -Lo spacedatanetwork-edge \
  https://github.com/.../spacedatanetwork-edge-linux-arm64

# Configure for minimal resources
cat > ~/.sdn-edge/config.toml << EOF
[relay]
max_reservations = 16

[resource_limits]
max_conns = 50
EOF
```

### Container Size

The edge binary is significantly smaller than the full node:

| Binary | Size |
|--------|------|
| Full Node | ~50 MB |
| Edge Relay | ~15 MB |

## Troubleshooting

### Browsers Can't Connect

1. Check WebSocket port is accessible
2. Verify CORS configuration
3. Test with WebSocket client:

```bash
websocat ws://localhost:4002/
```

### TLS Certificate Issues

For Let's Encrypt with auto-renewal:

```bash
certbot certonly --standalone -d relay.spacedatanetwork.org
```

Update config:

```toml
[tls]
cert_file = "/etc/letsencrypt/live/relay.spacedatanetwork.org/fullchain.pem"
key_file = "/etc/letsencrypt/live/relay.spacedatanetwork.org/privkey.pem"
```

### High Latency

- Deploy edge relays geographically close to users
- Use QUIC transport where possible
- Enable connection pooling

## Next Steps

- [Browser Usage](/guide/js-browser) - Build browser applications
- [Full Node Setup](/guide/full-node) - Run a complete node
- [Configuration Reference](/guide/configuration) - All config options
