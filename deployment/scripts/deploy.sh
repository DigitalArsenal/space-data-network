#!/bin/bash
# SDN Deployment Script
# Deploys SDN nodes to remote servers via SSH

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_ROOT="$(dirname "$DEPLOY_DIR")"
CONFIG_FILE="${DEPLOY_DIR}/config/servers.yaml"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

usage() {
    cat << EOF
Usage: $0 [OPTIONS] COMMAND [TYPE]

Commands:
    deploy      Deploy to servers (optionally specify type: full, edge, registry)
    status      Check status of all servers
    logs        Fetch logs from servers
    stop        Stop services on servers
    restart     Restart services on servers

Options:
    -c, --config FILE    Config file (default: config/servers.yaml)
    -k, --key FILE       SSH key file (overrides config)
    -u, --user USER      SSH user (overrides config)
    -d, --docker         Use Docker deployment (default)
    -b, --binary         Use binary deployment
    -n, --dry-run        Show what would be done
    -h, --help           Show this help

Examples:
    $0 deploy              # Deploy all server types
    $0 deploy edge         # Deploy only edge relays
    $0 status              # Check all server status
    $0 logs full           # Get logs from full nodes
EOF
    exit 1
}

# Parse YAML (basic parser for our format)
parse_yaml() {
    local file=$1
    local prefix=$2
    local s='[[:space:]]*'
    local w='[a-zA-Z0-9_]*'
    sed -ne "s|^\($s\)\($w\)$s:$s\"\(.*\)\"$s\$|\1\2=\"\3\"|p" \
        -e "s|^\($s\)\($w\)$s:$s\(.*\)$s\$|\1\2=\"\3\"|p" "$file"
}

# Get servers of a specific type from config
get_servers() {
    local type=$1
    # Using yq if available, otherwise basic grep
    if command -v yq &> /dev/null; then
        yq e ".${type}[] | .ip" "$CONFIG_FILE" 2>/dev/null
    else
        grep -A 100 "^${type}:" "$CONFIG_FILE" | grep "ip:" | head -20 | awk '{print $2}'
    fi
}

# SSH to a server
ssh_cmd() {
    local ip=$1
    shift
    ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=10 "${SSH_USER}@${ip}" "$@"
}

# SCP to a server
scp_cmd() {
    local src=$1
    local ip=$2
    local dest=$3
    scp -i "$SSH_KEY" -o StrictHostKeyChecking=no "$src" "${SSH_USER}@${ip}:${dest}"
}

# Deploy Docker container to server
deploy_docker() {
    local ip=$1
    local type=$2
    local name=$3

    log_info "Deploying $type to $ip ($name)..."

    # Create deployment directory
    ssh_cmd "$ip" "mkdir -p /opt/sdn"

    # Copy docker-compose and Dockerfiles
    scp_cmd "${DEPLOY_DIR}/docker/Dockerfile.${type}" "$ip" "/opt/sdn/"

    # Generate docker-compose for single service
    cat << EOF | ssh_cmd "$ip" "cat > /opt/sdn/docker-compose.yaml"
version: '3.8'
services:
  sdn-${type}:
    build:
      context: /opt/sdn
      dockerfile: Dockerfile.${type}
    container_name: sdn-${type}
    restart: unless-stopped
    network_mode: host
    volumes:
      - sdn-data:/home/sdn/.spacedatanetwork
volumes:
  sdn-data:
EOF

    # Build and start
    ssh_cmd "$ip" "cd /opt/sdn && docker compose up -d --build"

    log_success "Deployed $type to $ip"
}

# Deploy binary to server
deploy_binary() {
    local ip=$1
    local type=$2
    local name=$3
    local binary

    case $type in
        full) binary="spacedatanetwork" ;;
        edge) binary="spacedatanetwork-edge" ;;
        registry) binary="registry-builder" ;;
    esac

    log_info "Deploying $binary to $ip ($name)..."

    # Build binary for Linux
    log_info "Building $binary for linux/amd64..."
    (cd "${PROJECT_ROOT}/sdn-server" && \
        GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "/tmp/${binary}" "./cmd/${binary}")

    # Copy binary
    ssh_cmd "$ip" "mkdir -p /opt/sdn/bin"
    scp_cmd "/tmp/${binary}" "$ip" "/opt/sdn/bin/"
    ssh_cmd "$ip" "chmod +x /opt/sdn/bin/${binary}"

    # Create systemd service
    cat << EOF | ssh_cmd "$ip" "cat > /etc/systemd/system/sdn-${type}.service"
[Unit]
Description=Space Data Network ${type}
After=network.target

[Service]
Type=simple
User=root
ExecStart=/opt/sdn/bin/${binary} daemon
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    # Start service
    ssh_cmd "$ip" "systemctl daemon-reload && systemctl enable sdn-${type} && systemctl restart sdn-${type}"

    log_success "Deployed $binary to $ip"
}

# Check server status
check_status() {
    local ip=$1
    local type=$2
    local name=$3

    if ssh_cmd "$ip" "docker ps --filter name=sdn-${type} --format '{{.Status}}'" 2>/dev/null | grep -q "Up"; then
        echo -e "${GREEN}●${NC} $name ($ip) - Running"
    elif ssh_cmd "$ip" "systemctl is-active sdn-${type}" 2>/dev/null | grep -q "active"; then
        echo -e "${GREEN}●${NC} $name ($ip) - Running (systemd)"
    else
        echo -e "${RED}●${NC} $name ($ip) - Stopped/Unreachable"
    fi
}

# Get logs from server
get_logs() {
    local ip=$1
    local type=$2
    local lines=${3:-100}

    log_info "Fetching logs from $ip..."
    ssh_cmd "$ip" "docker logs --tail $lines sdn-${type} 2>&1" 2>/dev/null || \
    ssh_cmd "$ip" "journalctl -u sdn-${type} -n $lines --no-pager" 2>/dev/null
}

# Main deployment function
do_deploy() {
    local type=$1
    local types=("full" "edge" "registry")

    if [[ -n "$type" ]]; then
        types=("$type")
    fi

    for t in "${types[@]}"; do
        local yaml_key
        case $t in
            full) yaml_key="full_nodes" ;;
            edge) yaml_key="edge_relays" ;;
            registry) yaml_key="registry_builders" ;;
        esac

        log_info "Deploying ${t} nodes..."

        # Get servers (simplified - in production use proper YAML parser)
        while IFS= read -r line; do
            if [[ "$line" =~ ip:\ *([0-9.]+) ]]; then
                local ip="${BASH_REMATCH[1]}"
                local name="sdn-${t}-${ip##*.}"

                if [[ "$DRY_RUN" == "true" ]]; then
                    log_info "[DRY-RUN] Would deploy $t to $ip"
                else
                    if [[ "$USE_DOCKER" == "true" ]]; then
                        deploy_docker "$ip" "$t" "$name"
                    else
                        deploy_binary "$ip" "$t" "$name"
                    fi
                fi
            fi
        done < <(sed -n "/^${yaml_key}:/,/^[a-z]/p" "$CONFIG_FILE")
    done
}

# Parse arguments
SSH_KEY="${HOME}/.ssh/sdn_deploy_key"
SSH_USER="root"
USE_DOCKER="true"
DRY_RUN="false"
COMMAND=""
TYPE=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--config) CONFIG_FILE="$2"; shift 2 ;;
        -k|--key) SSH_KEY="$2"; shift 2 ;;
        -u|--user) SSH_USER="$2"; shift 2 ;;
        -d|--docker) USE_DOCKER="true"; shift ;;
        -b|--binary) USE_DOCKER="false"; shift ;;
        -n|--dry-run) DRY_RUN="true"; shift ;;
        -h|--help) usage ;;
        deploy|status|logs|stop|restart)
            COMMAND="$1"
            TYPE="${2:-}"
            shift
            [[ -n "$TYPE" ]] && shift
            ;;
        *) log_error "Unknown option: $1"; usage ;;
    esac
done

[[ -z "$COMMAND" ]] && usage

# Execute command
case $COMMAND in
    deploy)
        do_deploy "$TYPE"
        ;;
    status)
        log_info "Checking server status..."
        for t in full edge registry; do
            local yaml_key
            case $t in
                full) yaml_key="full_nodes" ;;
                edge) yaml_key="edge_relays" ;;
                registry) yaml_key="registry_builders" ;;
            esac

            echo -e "\n${BLUE}=== ${t^^} NODES ===${NC}"
            while IFS= read -r line; do
                if [[ "$line" =~ ip:\ *([0-9.]+) ]]; then
                    check_status "${BASH_REMATCH[1]}" "$t" "sdn-${t}"
                fi
            done < <(sed -n "/^${yaml_key}:/,/^[a-z]/p" "$CONFIG_FILE")
        done
        ;;
    logs)
        if [[ -z "$TYPE" ]]; then
            log_error "Please specify server type for logs"
            exit 1
        fi
        # Get first server of type
        ip=$(get_servers "${TYPE}_nodes" | head -1)
        [[ -z "$ip" ]] && ip=$(get_servers "${TYPE}_relays" | head -1)
        [[ -z "$ip" ]] && ip=$(get_servers "${TYPE}_builders" | head -1)
        get_logs "$ip" "$TYPE"
        ;;
    stop|restart)
        log_info "${COMMAND^}ing services..."
        for t in full edge registry; do
            local yaml_key
            case $t in
                full) yaml_key="full_nodes" ;;
                edge) yaml_key="edge_relays" ;;
                registry) yaml_key="registry_builders" ;;
            esac

            while IFS= read -r line; do
                if [[ "$line" =~ ip:\ *([0-9.]+) ]]; then
                    local ip="${BASH_REMATCH[1]}"
                    ssh_cmd "$ip" "docker compose -f /opt/sdn/docker-compose.yaml $COMMAND" 2>/dev/null || \
                    ssh_cmd "$ip" "systemctl $COMMAND sdn-${t}" 2>/dev/null
                fi
            done < <(sed -n "/^${yaml_key}:/,/^[a-z]/p" "$CONFIG_FILE")
        done
        ;;
esac

log_success "Done!"
