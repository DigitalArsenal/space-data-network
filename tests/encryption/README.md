# Phase 9: Encrypted Traffic Testing

Comprehensive test harnesses demonstrating encrypted traffic between all node types in the Space Data Network.

## Test Categories

### 1. Browser-to-Server Encryption Test
- Browser (sdn-js) sends ECIES-encrypted FlatBuffer to Go server (sdn-server)
- Verify X25519, secp256k1, and P-256 key exchange all work
- Test with OMM, CDM, EPM message types

### 2. Server-to-Server Encryption Test
- Two sdn-server instances exchange encrypted messages
- Test direct connection and relay-mediated connection
- Verify PubSub encrypted message broadcast

### 3. Edge Relay Pass-Through Test
- Verify edge relays correctly forward encrypted traffic without decryption
- Test circuit relay v2 with encrypted payloads
- Measure latency overhead

### 4. Desktop-to-Desktop Test
- Electron app (sdn-desktop) encrypted communication
- Test with large payload sizes (ephemeris data)

### 5. Mobile Wallet Browser Test
- Test Phantom/MetaMask in-app browser encrypted communication
- Verify Web3 wallet key derivation for encryption

## Running Tests

### Prerequisites
- Docker and Docker Compose
- Go 1.21+
- Node.js 18+
- Playwright (for browser tests)

### Quick Start

```bash
# Start test network
docker-compose -f docker-compose.test.yaml up -d

# Run Go server-to-server tests
go test -v ./go/...

# Run Playwright browser tests
cd playwright && npm install && npm test

# Run all tests
./run-tests.sh
```

### CI/CD

Tests are automatically run on:
- Pull requests to main branch
- Nightly builds
- Release tags

See `.github/workflows/encryption-tests.yml` for CI configuration.

## Test Infrastructure

```
tests/encryption/
├── docker-compose.test.yaml  # Test network with multiple node types
├── go/                       # Go test harnesses
│   ├── ecies/               # ECIES encryption tests
│   ├── server_to_server/    # Server-to-server tests
│   └── relay/               # Edge relay tests
├── playwright/              # Browser tests
│   ├── browser_to_server/   # Browser-to-server encryption
│   └── wallet/              # Web3 wallet tests
├── fixtures/                # Test data fixtures
│   ├── omm_sample.json
│   ├── cdm_sample.json
│   └── epm_sample.json
└── scripts/                 # Test helper scripts
    └── run-tests.sh
```

## Encryption Details

### ECIES Implementation
- Ephemeral key generation for each message
- Key exchange: X25519 / secp256k1 / P-256
- Symmetric encryption: AES-256-GCM
- MAC: HMAC-SHA256

### Message Format
```
[Routing Header (unencrypted)] + [ECIES Encrypted Payload]
  - schema_type: string
  - destination_peers: [string]
  - ttl: uint8
  - priority: uint8
  - encrypted: bool
```

### Key Types Tested
- X25519 (Curve25519) - Primary
- secp256k1 - Ethereum/Bitcoin compatible
- P-256 (NIST) - Hardware security module compatible
