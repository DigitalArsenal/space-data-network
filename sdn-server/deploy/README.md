# Space Data Network Deployment

This directory contains deployment configurations for the Space Data Network services.

## Systemd Services

### Edge Relay Service

The edge relay provides WebSocket and QUIC transport for browser clients without storing data.

```bash
# Install edge relay
sudo cp spacedatanetwork-edge.service /etc/systemd/system/
sudo mkdir -p /opt/spacedatanetwork/bin
sudo cp ../bin/spacedatanetwork-edge /opt/spacedatanetwork/bin/

# Create service user
sudo useradd -r -s /bin/false sdn

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable spacedatanetwork-edge
sudo systemctl start spacedatanetwork-edge

# Check status
sudo systemctl status spacedatanetwork-edge
sudo journalctl -u spacedatanetwork-edge -f
```

### Full Node Service

The full node stores SDS data and participates in the P2P network.

```bash
# Install full node
sudo cp spacedatanetwork.service /etc/systemd/system/
sudo mkdir -p /opt/spacedatanetwork/bin
sudo mkdir -p /etc/spacedatanetwork
sudo mkdir -p /var/lib/spacedatanetwork
sudo cp ../bin/spacedatanetwork /opt/spacedatanetwork/bin/

# Create config file
sudo tee /etc/spacedatanetwork/config.yaml <<EOF
mode: full
network:
  listen:
    - /ip4/0.0.0.0/tcp/4001
    - /ip4/0.0.0.0/tcp/8080/ws
    - /ip4/0.0.0.0/udp/4001/quic-v1
  bootstrap:
    - /dnsaddr/bootstrap.digitalarsenal.io/p2p/QmBootstrap1
  max_connections: 1000
  enable_relay: true
storage:
  path: /var/lib/spacedatanetwork/data
  max_size: 10GB
  gc_interval: 1h
schemas:
  validate: true
  strict: true
EOF

# Set permissions
sudo chown -R sdn:sdn /var/lib/spacedatanetwork
sudo chown -R sdn:sdn /etc/spacedatanetwork

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable spacedatanetwork
sudo systemctl start spacedatanetwork
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| SDN_MODE | Operating mode (full/edge) | full |
| SDN_CONFIG | Config file path | /etc/spacedatanetwork/config.yaml |
| SDN_DATA_DIR | Data directory | /var/lib/spacedatanetwork |
| SDN_LISTEN | Listen addresses | TCP 4001, WS 8080, QUIC 4001 |
| SDN_MAX_CONNECTIONS | Max peer connections | 1000 |
| SDN_HEALTH_PORT | Health check HTTP port | 9090 |

## Health Checks

Edge relay exposes health endpoint at `http://localhost:9090/health`:

```bash
curl http://localhost:9090/health
```

## Firewall Configuration

```bash
# Edge relay (WebSocket + QUIC)
sudo ufw allow 8080/tcp  # WebSocket
sudo ufw allow 4001/udp  # QUIC

# Full node (all transports)
sudo ufw allow 4001/tcp  # TCP
sudo ufw allow 8080/tcp  # WebSocket
sudo ufw allow 4001/udp  # QUIC
```

## Docker Deployment

See the Dockerfile in the project root for containerized deployment.
