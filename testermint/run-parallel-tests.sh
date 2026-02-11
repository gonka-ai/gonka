#!/bin/bash
set -e

# Parallel Local Testing with LXD
# Replicates GitHub Actions parallel testing workflow locally

#=============================================================================
# Configuration
#=============================================================================

PARALLEL_JOBS=3
TEST_FILTER=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/parallel-test-results"
LXD_IMAGE="gonka-test-runner"
CONTAINER_PREFIX="gonka-test"

#=============================================================================
# Color output
#=============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

#=============================================================================
# Parse arguments
#=============================================================================

while [[ $# -gt 0 ]]; do
    case $1 in
        --parallel)
            PARALLEL_JOBS="$2"
            shift 2
            ;;
        --tests)
            TEST_FILTER="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --parallel N    Number of parallel workers (default: 3)"
            echo "  --tests NAMES   Comma-separated test class names to run"
            echo "  -h, --help      Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                                    # Run all tests with 3 workers"
            echo "  $0 --parallel 8                       # Run with 8 workers"
            echo "  $0 --tests \"InferenceTests,GovernanceTests\""
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

#=============================================================================
# Cleanup function
#=============================================================================

cleanup() {
    log_info "Cleaning up..."
    # Stop and delete any remaining test containers
    lxc list --format=csv | grep "^${CONTAINER_PREFIX}-" | cut -d, -f1 | while read -r container; do
        log_info "Removing container: $container"
        lxc delete -f "$container" 2>/dev/null || true
    done
}

trap cleanup EXIT

#=============================================================================
# Prerequisites check
#=============================================================================

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    # Check LXD
    if ! command -v lxc &> /dev/null; then
        log_error "LXD is not installed. Please install it first:"
        log_error "  sudo snap install lxd"
        log_error "  sudo lxd init --auto"
        exit 1
    fi
    
    # Check if LXD is initialized
    if ! lxc list &> /dev/null; then
        log_error "LXD is not initialized. Please run: sudo lxd init --auto"
        exit 1
    fi
    
    # Check if golden image exists
    if ! lxc image list | grep -q "$LXD_IMAGE"; then
        log_error "Golden image '$LXD_IMAGE' not found!"
        log_error "Please build the golden image first:"
        log_error "  ./setup-base-image.sh"
        exit 1
    fi
    
    # Ensure LXD traffic is allowed in iptables FORWARD chain
    # This is needed when Docker is installed as it sets FORWARD policy to DROP
    if ! sudo iptables -C FORWARD -i lxdbr0 -j ACCEPT 2>/dev/null; then
        log_info "Adding iptables rules to allow LXD traffic..."
        sudo iptables -I FORWARD -i lxdbr0 -j ACCEPT
        sudo iptables -I FORWARD -o lxdbr0 -j ACCEPT
    fi
    
    # Check GNU parallel
    if ! command -v parallel &> /dev/null; then
        log_warn "GNU parallel is not installed. Installing..."
        sudo apt-get update && sudo apt-get install -y parallel
    fi
    
    log_success "All prerequisites satisfied"
}

#=============================================================================
# Test discovery
#=============================================================================

discover_tests() {
    log_info "Discovering test classes..."
    
    cd "${SCRIPT_DIR}"
    
    if [ -n "$TEST_FILTER" ]; then
        # Use provided test filter
        TEST_CLASSES=(${TEST_FILTER//,/ })
        log_info "Running specific tests: ${TEST_CLASSES[*]}"
    else
        # Discover all tests
        TEST_CLASSES_JSON=$(./gradlew -q listAllTestClasses)
        # Parse JSON array: ["Test1","Test2"] -> Test1 Test2
        TEST_CLASSES=$(echo "$TEST_CLASSES_JSON" | sed 's/\[//g' | sed 's/\]//g' | sed 's/"//g' | tr ',' ' ')
        TEST_CLASSES=($TEST_CLASSES)
        log_info "Discovered ${#TEST_CLASSES[@]} test classes"
    fi
    
    if [ ${#TEST_CLASSES[@]} -eq 0 ]; then
        log_error "No tests found to run"
        exit 1
    fi
    
    # Export for parallel
    export TEST_CLASSES_STR="${TEST_CLASSES[*]}"
}

#=============================================================================
# Run single test in LXD container
#=============================================================================

run_test_in_container() {
    local test_class="$1"
    local container_name="${CONTAINER_PREFIX}-${test_class}-$$-${RANDOM}"
    
    log_info "[$test_class] Starting test container: $container_name"
    
    # Create container from golden image (Docker images and Gradle already pre-loaded)
    lxc launch "$LXD_IMAGE" "$container_name" \
        -c limits.memory=8GB \
        -c limits.cpu=4 \
        -c security.nesting=true \
        -c security.privileged=true \
        -c raw.lxc="lxc.apparmor.profile=unconfined" \
        -c linux.kernel_modules="overlay,br_netfilter,ip_tables,ip6_tables,iptable_nat,xt_conntrack" \
        || {
            log_error "[$test_class] Failed to create container"
            return 1
        }
    
    # Wait for container to be ready
    log_info "[$test_class] Waiting for container to be ready..."
    local max_wait=30
    local count=0
    while ! lxc exec "$container_name" -- echo "ready" > /dev/null 2>&1; do
        sleep 1
        count=$((count + 1))
        if [ $count -ge $max_wait ]; then
            log_error "[$test_class] Container failed to start within ${max_wait}s"
            lxc delete -f "$container_name" 2>/dev/null || true
            return 1
        fi
    done
    
    log_info "[$test_class] Container ready after ${count}s"
    
    # Start Docker daemon (images already loaded from golden image)
    log_info "[$test_class] Starting Docker daemon..."
    lxc exec "$container_name" -- bash -c "
        service docker start || dockerd > /dev/null 2>&1 &
        sleep 3
    " > /dev/null 2>&1 || {
        log_error "[$test_class] Failed to start Docker"
        lxc delete -f "$container_name"
        return 1
    }
    
    log_info "[$test_class] Copying workspace..."
    
    # Copy workspace to container as /gonka (tests look for 'gonka' directory name)
    lxc exec "$container_name" -- mkdir -p /gonka
    
    # Create temporary tar file and push it (more reliable than piping)
    local temp_tar="/tmp/workspace-${test_class}-$$.tar.gz"
    cd "${WORKSPACE_DIR}"
    tar --exclude='node_modules' \
        --exclude='target' \
        --exclude='build' \
        --exclude='.gradle' \
        --exclude='prod-local' \
        --exclude='.parallel-test-temp' \
        --exclude='run-parallel-tests' \
        -czf "$temp_tar" .
    
    lxc file push "$temp_tar" "$container_name/tmp/workspace.tar.gz"
    lxc exec "$container_name" -- tar -xzf /tmp/workspace.tar.gz -C /gonka
    lxc exec "$container_name" -- rm /tmp/workspace.tar.gz
    rm -f "$temp_tar"
    
    # Configure git to trust the gonka directory
    lxc exec "$container_name" -- git config --global --add safe.directory /gonka
    
    log_info "[$test_class] Starting test chain..."
    
    # Launch test chain
    lxc exec "$container_name" -- bash -c "
        cd /gonka/local-test-net
        ./launch.sh
    " || {
        log_error "[$test_class] Failed to launch test chain"
        lxc delete -f "$container_name"
        return 1
    }
    
    log_info "[$test_class] Running tests..."
    
    # Run the test
    local exit_code=0
    lxc exec "$container_name" -- bash -c "
        cd /gonka
        make run-tests TESTS='$test_class'
    " || exit_code=$?
    
    if [ $exit_code -eq 0 ]; then
        log_success "[$test_class] Tests passed"
    else
        log_error "[$test_class] Tests failed (exit code: $exit_code)"
    fi
    
    log_info "[$test_class] Collecting results and artifacts..."
    
    # Create results directory for this test
    mkdir -p "${RESULTS_DIR}/${test_class}"
    
    # Copy test results
    lxc file pull -r "$container_name/gonka/testermint/build/test-results/" \
        "${RESULTS_DIR}/${test_class}/" 2>/dev/null || true
    
    # Copy test reports (HTML)
    lxc file pull -r "$container_name/gonka/testermint/build/reports/" \
        "${RESULTS_DIR}/${test_class}/" 2>/dev/null || true
    
    # Copy testermint logs
    lxc file pull -r "$container_name/gonka/testermint/logs/" \
        "${RESULTS_DIR}/${test_class}/testermint-logs/" 2>/dev/null || true
    
    # Copy Docker container logs from test chain
    log_info "[$test_class] Collecting Docker container logs..."
    lxc exec "$container_name" -- bash -c "
        mkdir -p /tmp/docker-logs
        cd /gonka/local-test-net
        # Get logs from all running containers
        docker-compose logs --no-color > /tmp/docker-logs/docker-compose.log 2>&1 || true
        docker ps -a --format '{{.Names}}' | while read container; do
            docker logs \$container > /tmp/docker-logs/\${container}.log 2>&1 || true
        done
    " 2>/dev/null || true
    
    # Pull Docker logs
    lxc file pull -r "$container_name/tmp/docker-logs/" \
        "${RESULTS_DIR}/${test_class}/" 2>/dev/null || true
    
    # Save container info
    lxc exec "$container_name" -- docker ps -a > "${RESULTS_DIR}/${test_class}/docker-ps.txt" 2>/dev/null || true
    lxc exec "$container_name" -- docker images > "${RESULTS_DIR}/${test_class}/docker-images.txt" 2>/dev/null || true
    
    log_success "[$test_class] Artifacts saved to ${RESULTS_DIR}/${test_class}"
    
    # Cleanup container
    log_info "[$test_class] Cleaning up container..."
    lxc delete -f "$container_name"
    
    log_info "[$test_class] Completed with exit code: $exit_code"
    
    return $exit_code
}

# Export function for parallel
export -f run_test_in_container
export -f log_info
export -f log_success
export -f log_error
export -f log_warn
export RESULTS_DIR
export WORKSPACE_DIR
export CONTAINER_PREFIX
export LXD_IMAGE
export RED GREEN YELLOW BLUE NC

#=============================================================================
# Aggregate results
#=============================================================================

aggregate_results() {
    log_info "Aggregating test results..."
    
    local total_passed=0
    local total_failed=0
    local total_skipped=0
    local failed_tests=""
    
    echo ""
    echo "========================================"
    echo "Test Results Summary"
    echo "========================================"
    echo ""
    printf "%-40s %8s %8s %8s\n" "Test Class" "Passed" "Failed" "Skipped"
    echo "------------------------------------------------------------------------"
    
    for test_class in "${TEST_CLASSES[@]}"; do
        local passed=0
        local failed=0
        local skipped=0
        
        # Find XML results
        local result_dir="${RESULTS_DIR}/${test_class}/test-results"
        
        if [ -d "$result_dir" ]; then
            for xml in "$result_dir"/**/*.xml; do
                if [ -f "$xml" ]; then
                    local tests=$(grep -o 'tests="[0-9]*"' "$xml" | grep -o '[0-9]*' || echo "0")
                    local failures=$(grep -o 'failures="[0-9]*"' "$xml" | grep -o '[0-9]*' || echo "0")
                    local skips=$(grep -o 'skipped="[0-9]*"' "$xml" | grep -o '[0-9]*' || echo "0")
                    
                    passed=$((passed + tests - failures - skips))
                    failed=$((failed + failures))
                    skipped=$((skipped + skips))
                fi
            done
        fi
        
        total_passed=$((total_passed + passed))
        total_failed=$((total_failed + failed))
        total_skipped=$((total_skipped + skipped))
        
        # Color code the output
        local status_color=""
        if [ $failed -gt 0 ]; then
            status_color="${RED}"
            failed_tests="${failed_tests}  - ${test_class}\n"
        else
            status_color="${GREEN}"
        fi
        
        printf "${status_color}%-40s %8s %8s %8s${NC}\n" \
            "$test_class" "$passed" "$failed" "$skipped"
    done
    
    echo "------------------------------------------------------------------------"
    printf "%-40s %8s %8s %8s\n" "TOTAL" "$total_passed" "$total_failed" "$total_skipped"
    echo ""
    
    if [ $total_failed -eq 0 ]; then
        log_success "All tests passed! ✅"
        echo ""
        echo "Total tests executed: $((total_passed + total_skipped))"
    else
        log_error "Some tests failed ❌"
        echo ""
        echo "Failed test classes:"
        echo -e "$failed_tests"
        echo "$total_failed test(s) failed out of $((total_passed + total_failed + total_skipped)) total"
    fi
    
    echo ""
    echo "Results saved to: ${RESULTS_DIR}"
    echo ""
    
    return $total_failed
}

#=============================================================================
# Main execution
#=============================================================================

main() {
    log_info "Starting parallel test execution with $PARALLEL_JOBS workers"
    
    # Create results directory
    mkdir -p "${RESULTS_DIR}"
    
    # Check prerequisites
    check_prerequisites
    
    # Discover tests
    discover_tests
    
    # Run tests in parallel
    log_info "Running ${#TEST_CLASSES[@]} tests with $PARALLEL_JOBS parallel workers..."
    echo ""
    
    # Use GNU parallel to run tests
    printf '%s\n' "${TEST_CLASSES[@]}" | \
        parallel --line-buffer -j "$PARALLEL_JOBS" \
        run_test_in_container {}
    
    local parallel_exit=$?
    
    echo ""
    
    # Aggregate results
    aggregate_results
    local aggregate_exit=$?
    
    if [ $aggregate_exit -eq 0 ]; then
        log_success "Parallel test execution completed successfully!"
        exit 0
    else
        log_error "Parallel test execution completed with failures"
        exit 1
    fi
}

# Run main
main

