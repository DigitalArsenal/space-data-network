# Downloads

Download the latest Space Data Network binaries and SDK packages.

## Server Binaries

### Full Node

The full node includes all features: data storage, DHT participation, PubSub, and relay capabilities.

<div class="download-grid">

| Platform | Architecture | Download | Size |
|----------|--------------|----------|------|
| **Linux** | x86_64 (AMD64) | [spacedatanetwork-linux-amd64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-linux-amd64) | ~25 MB |
| **Linux** | ARM64 | [spacedatanetwork-linux-arm64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-linux-arm64) | ~24 MB |
| **macOS** | Apple Silicon (ARM64) | [spacedatanetwork-darwin-arm64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-darwin-arm64) | ~26 MB |
| **macOS** | Intel (AMD64) | [spacedatanetwork-darwin-amd64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-darwin-amd64) | ~27 MB |
| **Windows** | x86_64 (AMD64) | [spacedatanetwork-windows-amd64.exe](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-windows-amd64.exe) | ~26 MB |

</div>

### Edge Relay (Lightweight)

The edge relay binary is optimized for minimal resource usage. Ideal for embedded systems or dedicated relay servers.

<div class="download-grid">

| Platform | Architecture | Download | Size |
|----------|--------------|----------|------|
| **Linux** | x86_64 (AMD64) | [spacedatanetwork-edge-linux-amd64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-edge-linux-amd64) | ~15 MB |
| **Linux** | ARM64 | [spacedatanetwork-edge-linux-arm64](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-edge-linux-arm64) | ~14 MB |
| **Linux** | ARMv7 (32-bit) | [spacedatanetwork-edge-linux-arm](https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-edge-linux-arm) | ~13 MB |

</div>

## Quick Install Script

### Linux / macOS

```bash
curl -sSL https://spacedatanetwork.org/install.sh | bash
```

This script will:
- Detect your platform and architecture
- Download the appropriate binary
- Install to `/usr/local/bin`
- Verify the download checksum

### Manual Installation

```bash
# Download
curl -Lo spacedatanetwork https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-linux-amd64

# Make executable
chmod +x spacedatanetwork

# Move to PATH
sudo mv spacedatanetwork /usr/local/bin/

# Verify
spacedatanetwork version
```

## JavaScript SDK

### npm / yarn / pnpm

::: code-group

```bash [npm]
npm install @spacedatanetwork/sdn-js
```

```bash [yarn]
yarn add @spacedatanetwork/sdn-js
```

```bash [pnpm]
pnpm add @spacedatanetwork/sdn-js
```

:::

### CDN (Browser)

```html
<script type="module">
  import { SDNNode } from 'https://cdn.spacedatanetwork.org/sdn-js/latest/index.esm.js';

  const node = new SDNNode();
  await node.start();
</script>
```

### Package Details

| Package | Version | Links |
|---------|---------|-------|
| `@spacedatanetwork/sdn-js` | [![npm](https://img.shields.io/npm/v/@spacedatanetwork/sdn-js)](https://www.npmjs.com/package/@spacedatanetwork/sdn-js) | [npm](https://www.npmjs.com/package/@spacedatanetwork/sdn-js) · [GitHub](https://github.com/DigitalArsenal/go-space-data-network/tree/main/sdn-js) |

## Docker Images

### Full Node

```bash
# Pull the latest image
docker pull ghcr.io/digitalarsenal/spacedatanetwork:latest

# Run with persistent storage
docker run -d \
  --name sdn-node \
  -p 4001:4001 \
  -p 4001:4001/udp \
  -v sdn-data:/data \
  ghcr.io/digitalarsenal/spacedatanetwork:latest
```

### Edge Relay

```bash
docker pull ghcr.io/digitalarsenal/spacedatanetwork-edge:latest

docker run -d \
  --name sdn-edge \
  -p 4001:4001 \
  -p 4002:4002 \
  ghcr.io/digitalarsenal/spacedatanetwork-edge:latest
```

### Docker Compose

```yaml
version: '3.8'

services:
  sdn-node:
    image: ghcr.io/digitalarsenal/spacedatanetwork:latest
    ports:
      - "4001:4001"
      - "4001:4001/udp"
      - "5001:5001"  # API
    volumes:
      - sdn-data:/data
    restart: unless-stopped

volumes:
  sdn-data:
```

## Build from Source

### Prerequisites

- Go 1.21+
- Node.js 18+ (for JavaScript SDK)
- Git

### Server

```bash
git clone https://github.com/DigitalArsenal/go-space-data-network.git
cd go-space-data-network/sdn-server

# Full node
go build -o spacedatanetwork ./cmd/spacedatanetwork

# Edge relay
go build -tags edge -o spacedatanetwork-edge ./cmd/spacedatanetwork-edge
```

### JavaScript SDK

```bash
cd sdn-js
npm install
npm run build
```

### WASM Components

Requires Emscripten:

```bash
# Setup Emscripten
./scripts/setup-emsdk.sh
source packages/emsdk/emsdk_env.sh

# Build WASM modules
npx tsx scripts/build-edge-registry.ts sample-relays.json
```

## Checksums

All releases include SHA256 checksums. Verify your download:

```bash
# Download checksum file
curl -Lo checksums.txt https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/checksums.txt

# Verify (Linux)
sha256sum -c checksums.txt --ignore-missing

# Verify (macOS)
shasum -a 256 -c checksums.txt --ignore-missing
```

## System Requirements

### Full Node

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 1 core | 2+ cores |
| RAM | 512 MB | 2 GB |
| Storage | 1 GB | 10+ GB |
| Network | 1 Mbps | 10+ Mbps |

### Edge Relay

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPU | 1 core | 1 core |
| RAM | 128 MB | 256 MB |
| Storage | 100 MB | 500 MB |
| Network | 1 Mbps | 10+ Mbps |

## Release History

See the [GitHub Releases](https://github.com/DigitalArsenal/go-space-data-network/releases) page for the full changelog.

## Need Help?

- [Getting Started Guide](/guide/getting-started)
- [Full Node Setup](/guide/full-node)
- [GitHub Issues](https://github.com/DigitalArsenal/go-space-data-network/issues)

<style>
.download-grid table {
  width: 100%;
}

.download-grid a {
  font-weight: 600;
}
</style>
