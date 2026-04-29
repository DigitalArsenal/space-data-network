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
        yq e ".${type}[] | .host // .ip" "$CONFIG_FILE" 2>/dev/null
    else
        grep -A 100 "^${type}:" "$CONFIG_FILE" | grep -E "^[[:space:]]+(host|ip):" | head -20 | awk '{print $2}'
    fi
}

parse_server_endpoint() {
    local line=$1
    if [[ "$line" =~ (host|ip):[[:space:]]*([^[:space:]]+) ]]; then
        printf '%s\n' "${BASH_REMATCH[2]}"
    fi
}

# SSH to a server
ssh_cmd() {
    local ip=$1
    shift
    local ssh_opts=(-o StrictHostKeyChecking=no -o ConnectTimeout=10)
    if [[ -n "${SSH_KEY:-}" && -f "$SSH_KEY" ]]; then
        ssh_opts=(-i "$SSH_KEY" "${ssh_opts[@]}")
    fi
    ssh "${ssh_opts[@]}" "${SSH_USER}@${ip}" "$@"
}

# SCP to a server
scp_cmd() {
    local src=$1
    local ip=$2
    local dest=$3
    local ssh_opts=(-o StrictHostKeyChecking=no)
    if [[ -n "${SSH_KEY:-}" && -f "$SSH_KEY" ]]; then
        ssh_opts=(-i "$SSH_KEY" "${ssh_opts[@]}")
    fi
    scp "${ssh_opts[@]}" "$src" "${SSH_USER}@${ip}:${dest}"
}

# Rsync a directory or file to a server
rsync_cmd() {
    local src=$1
    local ip=$2
    local dest=$3
    local transport="ssh -o StrictHostKeyChecking=no -o ConnectTimeout=10"
    if [[ -n "${SSH_KEY:-}" && -f "$SSH_KEY" ]]; then
        transport="ssh -i ${SSH_KEY} -o StrictHostKeyChecking=no -o ConnectTimeout=10"
    fi
    rsync -az --delete -e "$transport" "$src" "${SSH_USER}@${ip}:${dest}"
}

service_name() {
    case "$1" in
        full) echo "space-data-network" ;;
        *) echo "sdn-$1" ;;
    esac
}

full_node_service_name() {
    local ip=$1

    if ssh_cmd "$ip" "systemctl list-unit-files space-data-network.service >/dev/null 2>&1 || test -f /etc/systemd/system/space-data-network.service" >/dev/null 2>&1; then
        echo "space-data-network"
    else
        echo "spacedatanetwork"
    fi
}

prepare_full_node_assets() {
    log_info "Building shared admin shell assets..."
    (cd "${PROJECT_ROOT}/sdn-js" && npm run build:ui)

    if [[ ! -f "${PROJECT_ROOT}/webui/build/index.html" ]]; then
        log_error "webui/build is missing. Build the IPFS WebUI before deploying a full node."
        exit 1
    fi
}

find_licensing_module_wasm() {
    local candidates=(
        "${PROJECT_ROOT}/../space-data-network-plugins/licensing/core/dist/isomorphic/module.wasm"
        "${PROJECT_ROOT}/../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm"
    )
    local candidate
    for candidate in "${candidates[@]}"; do
        if [[ -f "$candidate" ]]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

deploy_full_node_licensing_module() {
    local ip=$1
    local service=$2
    local module_path

    if ! module_path="$(find_licensing_module_wasm)"; then
        log_warn "Licensing module wasm not found; leaving existing remote ORBPRO_LICENSING_WASM_PATH artifact unchanged."
        return
    fi

    log_info "Deploying licensing module wasm from ${module_path}..."
    ssh_cmd "$ip" "mkdir -p /opt/spacedatanetwork/wasm /etc/systemd/system/${service}.service.d"
    scp_cmd "$module_path" "$ip" "/opt/spacedatanetwork/wasm/licensing-module.wasm"
    ssh_cmd "$ip" "chown sdn:sdn /opt/spacedatanetwork/wasm/licensing-module.wasm && chmod 644 /opt/spacedatanetwork/wasm/licensing-module.wasm && cat > /etc/systemd/system/${service}.service.d/licensing.conf <<'EOF'
[Service]
Environment=ORBPRO_LICENSING_WASM_PATH=/opt/spacedatanetwork/wasm/licensing-module.wasm
EOF"
}

load_tracked_dev_wallet() {
    local wallet_config="${PROJECT_ROOT}/config/dev-wallet.env"
    if [[ ! -f "${wallet_config}" ]]; then
        return
    fi

    # shellcheck disable=SC1090
    source "${wallet_config}"
}

assert_prod_config_excludes_tracked_dev_wallet() {
    local config_path=$1
    local config_label=$2

    load_tracked_dev_wallet

    if [[ -z "${SDN_TRACKED_DEV_ADMIN_XPUB:-}" ]]; then
        return
    fi

    if grep -Fq "${SDN_TRACKED_DEV_ADMIN_XPUB}" "${config_path}"; then
        log_error "Refusing to deploy ${config_label}: it contains the tracked local dev wallet xpub from config/dev-wallet.env"
        exit 1
    fi
}

# Deploy Docker container to server
deploy_docker() {
    local ip=$1
    local type=$2
    local name=$3

    log_info "Deploying $type to $ip ($name)..."

    if [[ "$type" == "full" ]]; then
        prepare_full_node_assets
        assert_prod_config_excludes_tracked_dev_wallet "${PROJECT_ROOT}/config/full-docker.yaml" "config/full-docker.yaml"

        ssh_cmd "$ip" "rm -rf /opt/sdn && mkdir -p /opt/sdn/deployment/docker /opt/sdn/sdn-server /opt/sdn/sdn-js/ui/dist /opt/sdn/webui/build /opt/sdn/config /opt/sdn/scripts"

        rsync_cmd "${PROJECT_ROOT}/deployment/docker/Dockerfile.full" "$ip" "/opt/sdn/deployment/docker/Dockerfile.full"
        rsync_cmd "${PROJECT_ROOT}/sdn-server/" "$ip" "/opt/sdn/sdn-server/"
        rsync_cmd "${PROJECT_ROOT}/sdn-js/ui/dist/" "$ip" "/opt/sdn/sdn-js/ui/dist/"
        rsync_cmd "${PROJECT_ROOT}/webui/build/" "$ip" "/opt/sdn/webui/build/"
        rsync_cmd "${PROJECT_ROOT}/config/full-docker.yaml" "$ip" "/opt/sdn/config/full-docker.yaml"
        rsync_cmd "${PROJECT_ROOT}/scripts/install-wasmedge.sh" "$ip" "/opt/sdn/scripts/install-wasmedge.sh"

        cat << EOF | ssh_cmd "$ip" "cat > /opt/sdn/docker-compose.yaml"
version: '3.8'
services:
  sdn-full:
    build:
      context: /opt/sdn
      dockerfile: deployment/docker/Dockerfile.full
    container_name: sdn-full
    restart: unless-stopped
    network_mode: host
    volumes:
      - sdn-data:/app/data
volumes:
  sdn-data:
EOF

        ssh_cmd "$ip" "cd /opt/sdn && docker compose up -d --build"
        log_success "Deployed full to $ip"
        return
    fi

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

    if [[ "$type" == "full" ]]; then
        prepare_full_node_assets
        assert_prod_config_excludes_tracked_dev_wallet "${PROJECT_ROOT}/config/full-vm.yaml" "config/full-vm.yaml"
        log_info "Deploying full node bundle to $ip ($name)..."

        local full_service
        local config_dir
        full_service="$(full_node_service_name "$ip")"
        config_dir="/etc/spacedatanetwork"
        if [[ "$full_service" == "space-data-network" ]]; then
            config_dir="/etc/space-data-network"
        fi

        ssh_cmd "$ip" "mkdir -p /opt/spacedatanetwork/bin /opt/spacedatanetwork/admin-ui /opt/spacedatanetwork/webui /opt/spacedatanetwork/sdn-server /opt/spacedatanetwork/scripts ${config_dir} /var/lib/spacedatanetwork/frontend /var/lib/spacedatanetwork/data && id -u sdn >/dev/null 2>&1 || useradd --system --home /var/lib/spacedatanetwork --shell /usr/sbin/nologin sdn"

        rsync_cmd "${PROJECT_ROOT}/sdn-server/" "$ip" "/opt/spacedatanetwork/sdn-server/"
        rsync_cmd "${PROJECT_ROOT}/scripts/" "$ip" "/opt/spacedatanetwork/scripts/"
        rsync_cmd "${PROJECT_ROOT}/sdn-js/ui/dist/" "$ip" "/opt/spacedatanetwork/admin-ui/"
        rsync_cmd "${PROJECT_ROOT}/webui/build/" "$ip" "/opt/spacedatanetwork/webui/"
        deploy_full_node_licensing_module "$ip" "$full_service"
        if [[ "$full_service" == "spacedatanetwork" ]] || ! ssh_cmd "$ip" "test -f ${config_dir}/config.yaml" >/dev/null 2>&1; then
            rsync_cmd "${PROJECT_ROOT}/config/full-vm.yaml" "$ip" "${config_dir}/config.yaml"
        else
            log_info "Preserving existing full-node config at ${config_dir}/config.yaml"
        fi
        if [[ "$full_service" == "spacedatanetwork" ]]; then
            rsync_cmd "${PROJECT_ROOT}/sdn-server/deploy/spacedatanetwork.service" "$ip" "/etc/systemd/system/spacedatanetwork.service"
        fi

        ssh_cmd "$ip" "chmod 755 ${config_dir} && chown root:root ${config_dir}/config.yaml && chmod 644 ${config_dir}/config.yaml && chmod +x /opt/spacedatanetwork/scripts/install-wasmedge.sh /opt/spacedatanetwork/scripts/go-with-wasmedge.sh && WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge /opt/spacedatanetwork/scripts/go-with-wasmedge.sh build -o /opt/spacedatanetwork/bin/spacedatanetwork ./cmd/spacedatanetwork && chown -R sdn:sdn /opt/spacedatanetwork /var/lib/spacedatanetwork && systemctl daemon-reload && systemctl enable ${full_service} && systemctl restart ${full_service} && if [ '${full_service}' = 'space-data-network' ]; then systemctl disable --now spacedatanetwork >/dev/null 2>&1 || true; fi"

        log_success "Deployed full node bundle to $ip"
        return
    fi

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
    elif ssh_cmd "$ip" "systemctl is-active $(service_name "$type") || { [ '$type' = full ] && systemctl is-active spacedatanetwork; }" 2>/dev/null | grep -q "active"; then
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
    ssh_cmd "$ip" "journalctl -u $(service_name "$type") -n $lines --no-pager || { [ '$type' = full ] && journalctl -u spacedatanetwork -n $lines --no-pager; }" 2>/dev/null
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
            local endpoint
            endpoint="$(parse_server_endpoint "$line")"
            if [[ -n "$endpoint" ]]; then
                local name="sdn-${t}-${endpoint##*.}"

                if [[ "$DRY_RUN" == "true" ]]; then
                    log_info "[DRY-RUN] Would deploy $t to $endpoint"
                else
                    if [[ "$USE_DOCKER" == "true" ]]; then
                        deploy_docker "$endpoint" "$t" "$name"
                    else
                        deploy_binary "$endpoint" "$t" "$name"
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
            yaml_key=""
            case $t in
                full) yaml_key="full_nodes" ;;
                edge) yaml_key="edge_relays" ;;
                registry) yaml_key="registry_builders" ;;
            esac

            echo -e "\n${BLUE}=== ${t^^} NODES ===${NC}"
            while IFS= read -r line; do
                endpoint="$(parse_server_endpoint "$line")"
                if [[ -n "$endpoint" ]]; then
                    check_status "$endpoint" "$t" "sdn-${t}"
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
            yaml_key=""
            case $t in
                full) yaml_key="full_nodes" ;;
                edge) yaml_key="edge_relays" ;;
                registry) yaml_key="registry_builders" ;;
            esac

            while IFS= read -r line; do
                endpoint="$(parse_server_endpoint "$line")"
                if [[ -n "$endpoint" ]]; then
                    ssh_cmd "$endpoint" "docker compose -f /opt/sdn/docker-compose.yaml $COMMAND" 2>/dev/null || \
                    ssh_cmd "$endpoint" "systemctl $COMMAND $(service_name "$t")" 2>/dev/null
                fi
            done < <(sed -n "/^${yaml_key}:/,/^[a-z]/p" "$CONFIG_FILE")
        done
        ;;
esac

log_success "Done!"
