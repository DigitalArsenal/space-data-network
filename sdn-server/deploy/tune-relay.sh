#!/usr/bin/env bash
# tune-relay.sh — Kernel tuning for SDN edge relays handling 50,000+ concurrent connections.
# Run as root: sudo bash tune-relay.sh
set -euo pipefail

SYSCTL_CONF="/etc/sysctl.d/99-sdn-relay.conf"

echo "Writing kernel tuning to ${SYSCTL_CONF} ..."

cat > "${SYSCTL_CONF}" <<'EOF'
# SDN Edge Relay — sysctl tuning for 50k+ concurrent connections

# Maximum number of open file descriptors system-wide
fs.file-max = 200000

# TCP listen backlog
net.core.somaxconn = 65535
net.ipv4.tcp_max_syn_backlog = 65535

# Ephemeral port range (more outbound ports)
net.ipv4.ip_local_port_range = 1024 65535

# Reuse TIME_WAIT sockets for new connections
net.ipv4.tcp_tw_reuse = 1

# Reduce FIN_WAIT timeout
net.ipv4.tcp_fin_timeout = 15

# Network device backlog
net.core.netdev_max_backlog = 65535

# TCP buffer sizes (min, default, max) — allow large buffers for high-throughput relay
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.ipv4.tcp_rmem = 4096 87380 16777216
net.ipv4.tcp_wmem = 4096 65536 16777216
EOF

echo "Applying sysctl settings ..."
sysctl --system

echo "Done. Kernel tuned for 50k+ connections."
echo "Ensure your systemd service has LimitNOFILE=200000 (see spacedatanetwork-edge.service)."
