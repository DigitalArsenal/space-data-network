#!/bin/bash
# SDN Local Cluster Management
# Manage local Docker cluster for testing

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_ROOT="$(dirname "$DEPLOY_DIR")"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

usage() {
    cat << EOF
SDN Local Cluster Management

Usage: $0 COMMAND [OPTIONS]

Commands:
    up          Start the local cluster
    down        Stop and remove the cluster
    restart     Restart the cluster
    status      Show cluster status
    logs        Show logs (optionally specify service)
    ps          List running containers
    build       Build Docker images only
    clean       Remove all containers, images, and volumes
    shell       Open shell in a container
    test        Run integration tests against cluster

Options:
    -d, --detach    Run in background (for 'up')
    -f, --follow    Follow logs (for 'logs')
    -s, --service   Specify service name

Examples:
    $0 up -d                    # Start cluster in background
    $0 logs -f full-node-1      # Follow logs for full-node-1
    $0 shell edge-relay-us      # Open shell in edge relay
    $0 test                     # Run tests against cluster
EOF
    exit 1
}

# Change to deployment directory
cd "$DEPLOY_DIR"

# Commands
cmd_up() {
    local detach=""
    [[ "$1" == "-d" || "$1" == "--detach" ]] && detach="-d"

    log_info "Starting SDN local cluster..."
    docker compose up --build $detach

    if [[ -n "$detach" ]]; then
        log_info "Waiting for services to be healthy..."
        sleep 10
        cmd_status
    fi
}

cmd_down() {
    log_info "Stopping SDN local cluster..."
    docker compose down
    log_success "Cluster stopped"
}

cmd_restart() {
    log_info "Restarting SDN local cluster..."
    docker compose restart
    log_success "Cluster restarted"
}

cmd_status() {
    echo -e "\n${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║              SDN Local Cluster Status                        ║${NC}"
    echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}\n"

    # Full Nodes
    echo -e "${BLUE}Full Nodes:${NC}"
    for node in full-node-1 full-node-2; do
        local status=$(docker inspect -f '{{.State.Status}}' "sdn-${node##*-}" 2>/dev/null || echo "not found")
        local ip=$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "sdn-${node##*-}" 2>/dev/null || echo "N/A")
        if [[ "$status" == "running" ]]; then
            echo -e "  ${GREEN}●${NC} $node (${ip}) - Running"
        else
            echo -e "  ${RED}●${NC} $node - $status"
        fi
    done

    # Edge Relays
    echo -e "\n${BLUE}Edge Relays:${NC}"
    for relay in edge-relay-us edge-relay-eu edge-relay-asia; do
        local container="sdn-${relay##edge-relay-}"
        local status=$(docker inspect -f '{{.State.Status}}' "$container" 2>/dev/null || echo "not found")
        local ip=$(docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container" 2>/dev/null || echo "N/A")
        if [[ "$status" == "running" ]]; then
            # Check health endpoint
            local health=$(curl -s "http://localhost:${relay//[^0-9]/}91/health" 2>/dev/null || echo "")
            if [[ -n "$health" ]]; then
                echo -e "  ${GREEN}●${NC} $relay (${ip}) - Running (healthy)"
            else
                echo -e "  ${YELLOW}●${NC} $relay (${ip}) - Running (health check pending)"
            fi
        else
            echo -e "  ${RED}●${NC} $relay - $status"
        fi
    done

    # Registry Builder
    echo -e "\n${BLUE}Registry Builder:${NC}"
    local status=$(docker inspect -f '{{.State.Status}}' "sdn-registry" 2>/dev/null || echo "not found")
    if [[ "$status" == "running" ]]; then
        echo -e "  ${GREEN}●${NC} registry-builder - Running"
    else
        echo -e "  ${RED}●${NC} registry-builder - $status"
    fi

    # Network info
    echo -e "\n${BLUE}Network:${NC}"
    echo "  Subnet: 172.28.0.0/16"

    # Port mappings
    echo -e "\n${BLUE}Port Mappings:${NC}"
    echo "  Full Node 1:  localhost:4001 (TCP), localhost:8080 (WS)"
    echo "  Full Node 2:  localhost:4002 (TCP), localhost:8081 (WS)"
    echo "  Edge US:      localhost:8090 (WS), localhost:8091 (Health)"
    echo "  Edge EU:      localhost:8092 (WS), localhost:8093 (Health)"
    echo "  Edge Asia:    localhost:8094 (WS), localhost:8095 (Health)"

    echo ""
}

cmd_logs() {
    local follow=""
    local service=""

    while [[ $# -gt 0 ]]; do
        case $1 in
            -f|--follow) follow="-f"; shift ;;
            *) service="$1"; shift ;;
        esac
    done

    if [[ -n "$service" ]]; then
        docker compose logs $follow "$service"
    else
        docker compose logs $follow
    fi
}

cmd_ps() {
    docker compose ps
}

cmd_build() {
    log_info "Building Docker images..."
    docker compose build
    log_success "Images built"
}

cmd_clean() {
    log_warn "This will remove all SDN containers, images, and volumes!"
    read -p "Are you sure? (y/N) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        log_info "Cleaning up..."
        docker compose down -v --rmi all 2>/dev/null || true
        docker volume rm $(docker volume ls -q | grep sdn) 2>/dev/null || true
        log_success "Cleanup complete"
    fi
}

cmd_shell() {
    local service=$1
    [[ -z "$service" ]] && { log_error "Please specify a service"; exit 1; }
    docker compose exec "$service" /bin/sh
}

cmd_test() {
    log_info "Running integration tests against local cluster..."

    # Ensure cluster is running
    if ! docker compose ps | grep -q "running"; then
        log_warn "Cluster not running. Starting..."
        cmd_up -d
        sleep 15
    fi

    # Test 1: Check all services are running
    log_info "Test 1: Checking service health..."
    local all_healthy=true
    for container in sdn-full-1 sdn-full-2 sdn-edge-us sdn-edge-eu sdn-edge-asia; do
        if ! docker inspect -f '{{.State.Status}}' "$container" 2>/dev/null | grep -q "running"; then
            log_error "  $container is not running"
            all_healthy=false
        else
            log_success "  $container is running"
        fi
    done

    # Test 2: Check edge relay health endpoints
    log_info "Test 2: Checking edge relay health endpoints..."
    for port in 8091 8093 8095; do
        if curl -s "http://localhost:${port}/health" | grep -q "ok"; then
            log_success "  Edge relay on port $port is healthy"
        else
            log_warn "  Edge relay on port $port health check failed"
        fi
    done

    # Test 3: Check WebSocket connectivity
    log_info "Test 3: Checking WebSocket endpoints..."
    for port in 8080 8081 8090 8092 8094; do
        if curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}/" | grep -q "400\|426"; then
            log_success "  WebSocket endpoint on port $port is responding"
        else
            log_warn "  WebSocket endpoint on port $port not responding as expected"
        fi
    done

    # Test 4: Check peer connectivity (via logs)
    log_info "Test 4: Checking peer connections..."
    if docker logs sdn-full-2 2>&1 | grep -q "Connected to bootstrap peer\|peer:connect"; then
        log_success "  Peers are connecting"
    else
        log_warn "  No peer connections detected yet"
    fi

    echo ""
    if [[ "$all_healthy" == "true" ]]; then
        log_success "All basic tests passed!"
    else
        log_warn "Some tests failed - check logs for details"
    fi
}

# Parse arguments
[[ $# -eq 0 ]] && usage

COMMAND=$1
shift

case $COMMAND in
    up)      cmd_up "$@" ;;
    down)    cmd_down ;;
    restart) cmd_restart ;;
    status)  cmd_status ;;
    logs)    cmd_logs "$@" ;;
    ps)      cmd_ps ;;
    build)   cmd_build ;;
    clean)   cmd_clean ;;
    shell)   cmd_shell "$@" ;;
    test)    cmd_test ;;
    -h|--help) usage ;;
    *) log_error "Unknown command: $COMMAND"; usage ;;
esac
