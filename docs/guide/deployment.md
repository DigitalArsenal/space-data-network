# Deployment Guide

Production deployment strategies for Space Data Network nodes.

## Deployment Options

| Option | Best For | Complexity |
|--------|----------|------------|
| Systemd | Linux servers | Low |
| Docker | Containerized environments | Low |
| Kubernetes | Scale and orchestration | Medium |
| Cloud VM | Quick setup | Low |

## Systemd Deployment

### Create Service User

```bash
sudo useradd -r -s /bin/false -d /var/lib/sdn sdn
sudo mkdir -p /var/lib/sdn
sudo chown sdn:sdn /var/lib/sdn
```

### Install Binary

```bash
sudo curl -Lo /usr/local/bin/spacedatanetwork \
  https://github.com/DigitalArsenal/go-space-data-network/releases/latest/download/spacedatanetwork-linux-amd64
sudo chmod +x /usr/local/bin/spacedatanetwork
```

### Create Configuration

```bash
sudo mkdir -p /etc/sdn
sudo tee /etc/sdn/config.toml << 'EOF'
[identity]
# Will be generated on first run

[addresses]
swarm = [
  "/ip4/0.0.0.0/tcp/4001",
  "/ip4/0.0.0.0/udp/4001/quic-v1",
]
api = "/ip4/127.0.0.1/tcp/5001"

[storage]
path = "/var/lib/sdn/sdn.db"

[logging]
level = "info"
output = "/var/log/sdn/sdn.log"
EOF

sudo mkdir -p /var/log/sdn
sudo chown sdn:sdn /var/log/sdn
```

### Create Service File

```bash
sudo tee /etc/systemd/system/spacedatanetwork.service << 'EOF'
[Unit]
Description=Space Data Network Node
Documentation=https://spacedatanetwork.org/docs
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=sdn
Group=sdn
ExecStart=/usr/local/bin/spacedatanetwork daemon --config /etc/sdn/config.toml
Restart=always
RestartSec=5
TimeoutStartSec=60

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ReadWritePaths=/var/lib/sdn /var/log/sdn

# Resource limits
LimitNOFILE=65536
LimitNPROC=4096
MemoryMax=2G

[Install]
WantedBy=multi-user.target
EOF
```

### Enable and Start

```bash
sudo systemctl daemon-reload
sudo systemctl enable spacedatanetwork
sudo systemctl start spacedatanetwork
sudo systemctl status spacedatanetwork
```

### Log Management

```bash
# View logs
sudo journalctl -u spacedatanetwork -f

# Rotate logs
sudo tee /etc/logrotate.d/sdn << 'EOF'
/var/log/sdn/*.log {
    daily
    rotate 14
    compress
    delaycompress
    notifempty
    create 640 sdn sdn
    postrotate
        systemctl reload spacedatanetwork 2>/dev/null || true
    endscript
}
EOF
```

## Docker Deployment

### Single Container

```bash
docker run -d \
  --name sdn-node \
  --restart unless-stopped \
  -p 4001:4001 \
  -p 4001:4001/udp \
  -p 5001:5001 \
  -v sdn-data:/data \
  -v sdn-config:/config \
  ghcr.io/digitalarsenal/spacedatanetwork:latest
```

### Docker Compose

```yaml
# docker-compose.yml
version: '3.8'

services:
  sdn-node:
    image: ghcr.io/digitalarsenal/spacedatanetwork:latest
    container_name: sdn-node
    restart: unless-stopped
    ports:
      - "4001:4001"
      - "4001:4001/udp"
      - "5001:5001"
    volumes:
      - sdn-data:/data
      - ./config.toml:/config/config.toml:ro
    environment:
      - SDN_LOGGING_LEVEL=info
    healthcheck:
      test: ["CMD", "spacedatanetwork", "stats", "--json"]
      interval: 30s
      timeout: 10s
      retries: 3
    deploy:
      resources:
        limits:
          memory: 2G
          cpus: '2'

  sdn-edge:
    image: ghcr.io/digitalarsenal/spacedatanetwork-edge:latest
    container_name: sdn-edge
    restart: unless-stopped
    ports:
      - "4002:4002"
    depends_on:
      - sdn-node
    environment:
      - SDN_BOOTSTRAP_PEERS=/ip4/sdn-node/tcp/4001

volumes:
  sdn-data:
```

### Build Custom Image

```dockerfile
# Dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o spacedatanetwork ./cmd/spacedatanetwork

FROM alpine:3.18
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/spacedatanetwork /usr/local/bin/

EXPOSE 4001 4001/udp 5001
VOLUME /data

ENTRYPOINT ["spacedatanetwork"]
CMD ["daemon", "--config", "/data/config.toml"]
```

## Kubernetes Deployment

### Namespace

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: sdn
```

### ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: sdn-config
  namespace: sdn
data:
  config.toml: |
    [addresses]
    swarm = ["/ip4/0.0.0.0/tcp/4001", "/ip4/0.0.0.0/udp/4001/quic-v1"]
    api = "/ip4/0.0.0.0/tcp/5001"

    [storage]
    path = "/data/sdn.db"

    [metrics]
    enabled = true
    address = "0.0.0.0:9090"
```

### StatefulSet

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: sdn-node
  namespace: sdn
spec:
  serviceName: sdn
  replicas: 3
  selector:
    matchLabels:
      app: sdn-node
  template:
    metadata:
      labels:
        app: sdn-node
    spec:
      containers:
      - name: sdn
        image: ghcr.io/digitalarsenal/spacedatanetwork:latest
        args: ["daemon", "--config", "/config/config.toml"]
        ports:
        - containerPort: 4001
          name: swarm-tcp
        - containerPort: 4001
          protocol: UDP
          name: swarm-udp
        - containerPort: 5001
          name: api
        - containerPort: 9090
          name: metrics
        volumeMounts:
        - name: data
          mountPath: /data
        - name: config
          mountPath: /config
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "2Gi"
            cpu: "2"
        livenessProbe:
          httpGet:
            path: /health
            port: 5001
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /ready
            port: 5001
          initialDelaySeconds: 5
          periodSeconds: 5
      volumes:
      - name: config
        configMap:
          name: sdn-config
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      resources:
        requests:
          storage: 10Gi
```

### Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: sdn
  namespace: sdn
spec:
  type: LoadBalancer
  selector:
    app: sdn-node
  ports:
  - port: 4001
    targetPort: 4001
    name: swarm-tcp
  - port: 4001
    targetPort: 4001
    protocol: UDP
    name: swarm-udp
```

### ServiceMonitor (Prometheus)

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: sdn
  namespace: sdn
spec:
  selector:
    matchLabels:
      app: sdn-node
  endpoints:
  - port: metrics
    interval: 30s
```

## Cloud Deployments

### AWS EC2

```bash
# Launch instance
aws ec2 run-instances \
  --image-id ami-0c55b159cbfafe1f0 \
  --instance-type t3.medium \
  --key-name my-key \
  --security-group-ids sg-xxx \
  --user-data file://user-data.sh

# user-data.sh
#!/bin/bash
curl -Lo /usr/local/bin/spacedatanetwork \
  https://github.com/.../spacedatanetwork-linux-amd64
chmod +x /usr/local/bin/spacedatanetwork
spacedatanetwork init
spacedatanetwork daemon
```

### Google Cloud

```bash
gcloud compute instances create sdn-node \
  --machine-type=e2-medium \
  --image-family=ubuntu-2204-lts \
  --image-project=ubuntu-os-cloud \
  --metadata-from-file=startup-script=startup.sh
```

### Azure

```bash
az vm create \
  --resource-group sdn \
  --name sdn-node \
  --image Ubuntu2204 \
  --size Standard_B2s \
  --custom-data startup.sh
```

## High Availability

### Multi-Region Setup

Deploy nodes in multiple regions for resilience:

```
                    ┌─────────────────┐
                    │   DNS (GeoDNS)  │
                    └────────┬────────┘
                             │
        ┌────────────────────┼────────────────────┐
        │                    │                    │
┌───────▼───────┐   ┌───────▼───────┐   ┌───────▼───────┐
│  US-East Node │   │  EU-West Node │   │ Asia-Pac Node │
│   (Primary)   │◄──►   (Replica)   │◄──►   (Replica)   │
└───────────────┘   └───────────────┘   └───────────────┘
```

### Database Replication

For SQLite with Litestream:

```yaml
# litestream.yml
dbs:
  - path: /var/lib/sdn/sdn.db
    replicas:
      - type: s3
        bucket: sdn-backups
        path: sdn.db
        sync-interval: 1s
```

## Monitoring

### Prometheus Alerts

```yaml
groups:
- name: sdn
  rules:
  - alert: SDNNodeDown
    expr: up{job="sdn"} == 0
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "SDN node is down"

  - alert: SDNHighMemory
    expr: process_resident_memory_bytes{job="sdn"} > 1.5e9
    for: 10m
    labels:
      severity: warning

  - alert: SDNLowPeers
    expr: sdn_peers_connected < 3
    for: 5m
    labels:
      severity: warning
```

### Grafana Dashboard

Import the official SDN dashboard from Grafana.com or use the JSON in the repository.

## Security Checklist

- [ ] Run as non-root user
- [ ] Enable firewall (only open required ports)
- [ ] Use TLS for edge relays
- [ ] Restrict API access to localhost
- [ ] Enable rate limiting
- [ ] Set up log monitoring
- [ ] Configure automatic updates
- [ ] Back up identity keys
- [ ] Set resource limits

## Next Steps

- [Configuration Reference](/guide/configuration) - All config options
- [Security Best Practices](/guide/security-encryption) - Hardening
