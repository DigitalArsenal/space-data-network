#!/bin/bash
# SDN Encryption Test Runner
# Runs all encryption tests: Go tests, Playwright browser tests

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_ROOT="$(dirname "$(dirname "$TEST_DIR")")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
echo_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
echo_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
echo_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Default options
RUN_GO_TESTS=true
RUN_PLAYWRIGHT_TESTS=true
START_NETWORK=true
CLEANUP_AFTER=false
VERBOSE=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --go-only)
            RUN_PLAYWRIGHT_TESTS=false
            shift
            ;;
        --playwright-only)
            RUN_GO_TESTS=false
            shift
            ;;
        --no-network)
            START_NETWORK=false
            shift
            ;;
        --cleanup)
            CLEANUP_AFTER=true
            shift
            ;;
        --verbose|-v)
            VERBOSE=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [options]"
            echo ""
            echo "Options:"
            echo "  --go-only           Run only Go tests"
            echo "  --playwright-only   Run only Playwright tests"
            echo "  --no-network        Skip starting Docker network (assume running)"
            echo "  --cleanup           Stop network after tests"
            echo "  --verbose, -v       Verbose output"
            echo "  --help, -h          Show this help"
            exit 0
            ;;
        *)
            echo_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Change to test directory
cd "$TEST_DIR"

# Create results directory
mkdir -p test-results

echo_info "=========================================="
echo_info "SDN Encryption Test Suite"
echo_info "=========================================="
echo_info "Test directory: $TEST_DIR"
echo_info "Project root: $PROJECT_ROOT"
echo ""

# Start test network if requested
if [ "$START_NETWORK" = true ]; then
    echo_info "Starting test network..."

    docker-compose -f docker-compose.test.yaml up -d \
        server-alice server-bob server-carol edge-relay-1 edge-relay-2

    echo_info "Waiting for services to be healthy..."
    sleep 10

    # Wait for Alice to be ready
    RETRIES=30
    while [ $RETRIES -gt 0 ]; do
        if docker exec sdn-test-alice wget -q --spider http://localhost:8080 2>/dev/null; then
            echo_success "Server Alice is ready"
            break
        fi
        RETRIES=$((RETRIES - 1))
        sleep 2
    done

    if [ $RETRIES -eq 0 ]; then
        echo_error "Server Alice failed to start"
        docker-compose -f docker-compose.test.yaml logs server-alice
        exit 1
    fi
fi

# Initialize test results
GO_RESULT=0
PLAYWRIGHT_RESULT=0

# Run Go tests
if [ "$RUN_GO_TESTS" = true ]; then
    echo ""
    echo_info "=========================================="
    echo_info "Running Go Encryption Tests"
    echo_info "=========================================="

    cd "$TEST_DIR/go"

    if [ "$VERBOSE" = true ]; then
        go test -v -count=1 ./... 2>&1 | tee "$TEST_DIR/test-results/go-tests.log" || GO_RESULT=$?
    else
        go test -v -count=1 ./... > "$TEST_DIR/test-results/go-tests.log" 2>&1 || GO_RESULT=$?
    fi

    if [ $GO_RESULT -eq 0 ]; then
        echo_success "Go tests passed"
    else
        echo_error "Go tests failed (exit code: $GO_RESULT)"
        if [ "$VERBOSE" = false ]; then
            echo_info "See test-results/go-tests.log for details"
        fi
    fi

    cd "$TEST_DIR"
fi

# Run Playwright tests
if [ "$RUN_PLAYWRIGHT_TESTS" = true ]; then
    echo ""
    echo_info "=========================================="
    echo_info "Running Playwright Browser Tests"
    echo_info "=========================================="

    cd "$TEST_DIR/playwright"

    # Install dependencies if needed
    if [ ! -d "node_modules" ]; then
        echo_info "Installing Playwright dependencies..."
        npm install
        npx playwright install chromium
    fi

    # Set server URL for tests
    export SDN_SERVER_URL="http://localhost:18080"

    if [ "$VERBOSE" = true ]; then
        npx playwright test --reporter=list 2>&1 | tee "$TEST_DIR/test-results/playwright-tests.log" || PLAYWRIGHT_RESULT=$?
    else
        npx playwright test --reporter=json --output="$TEST_DIR/test-results/playwright-report" > "$TEST_DIR/test-results/playwright-tests.log" 2>&1 || PLAYWRIGHT_RESULT=$?
    fi

    if [ $PLAYWRIGHT_RESULT -eq 0 ]; then
        echo_success "Playwright tests passed"
    else
        echo_error "Playwright tests failed (exit code: $PLAYWRIGHT_RESULT)"
        if [ "$VERBOSE" = false ]; then
            echo_info "See test-results/playwright-tests.log for details"
        fi
    fi

    cd "$TEST_DIR"
fi

# Cleanup if requested
if [ "$CLEANUP_AFTER" = true ]; then
    echo ""
    echo_info "Cleaning up test network..."
    docker-compose -f docker-compose.test.yaml down -v
fi

# Summary
echo ""
echo_info "=========================================="
echo_info "Test Results Summary"
echo_info "=========================================="

TOTAL_RESULT=0

if [ "$RUN_GO_TESTS" = true ]; then
    if [ $GO_RESULT -eq 0 ]; then
        echo -e "Go Tests:        ${GREEN}PASSED${NC}"
    else
        echo -e "Go Tests:        ${RED}FAILED${NC}"
        TOTAL_RESULT=1
    fi
fi

if [ "$RUN_PLAYWRIGHT_TESTS" = true ]; then
    if [ $PLAYWRIGHT_RESULT -eq 0 ]; then
        echo -e "Playwright Tests: ${GREEN}PASSED${NC}"
    else
        echo -e "Playwright Tests: ${RED}FAILED${NC}"
        TOTAL_RESULT=1
    fi
fi

echo ""
echo_info "Test results saved to: $TEST_DIR/test-results/"

if [ $TOTAL_RESULT -eq 0 ]; then
    echo_success "All tests passed!"
else
    echo_error "Some tests failed"
fi

exit $TOTAL_RESULT
