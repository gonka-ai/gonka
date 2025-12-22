#!/bin/bash
set -e

# Cleanup script for parallel test runs
# Kills all LXD containers, Gradle daemons, and optionally removes test results

#=============================================================================
# Configuration
#=============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/parallel-test-results"
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

CLEAN_RESULTS=false
STOP_GRADLE=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --results)
            CLEAN_RESULTS=true
            shift
            ;;
        --gradle)
            STOP_GRADLE=true
            shift
            ;;
        --all)
            CLEAN_RESULTS=true
            STOP_GRADLE=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --results       Remove test results directory"
            echo "  --gradle        Stop Gradle daemon processes"
            echo "  --all           Clean everything (containers, results, gradle)"
            echo "  -h, --help      Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                    # Kill LXD containers only"
            echo "  $0 --results          # Kill containers and remove results"
            echo "  $0 --gradle           # Kill containers and stop Gradle daemons"
            echo "  $0 --all              # Full cleanup"
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

#=============================================================================
# Main cleanup
#=============================================================================

main() {
    log_info "Starting cleanup..."
    echo ""
    
    # 1. Clean up LXD containers
    log_info "Checking for test containers..."
    
    if command -v lxc &> /dev/null; then
        # Try without sudo first
        containers=$(lxc list --format=csv 2>/dev/null | grep "^${CONTAINER_PREFIX}-" | cut -d, -f1 || true)
        
        # If permission denied, try with sudo
        if [ -z "$containers" ] && lxc list &>/dev/null 2>&1 | grep -q "permission denied"; then
            containers=$(sudo lxc list --format=csv 2>/dev/null | grep "^${CONTAINER_PREFIX}-" | cut -d, -f1 || true)
            use_sudo=true
        else
            use_sudo=false
        fi
        
        if [ -n "$containers" ]; then
            log_info "Found test containers, removing..."
            echo "$containers" | while read -r container; do
                log_info "  Deleting: $container"
                if [ "$use_sudo" = true ]; then
                    sudo lxc delete -f "$container" 2>/dev/null || log_warn "Failed to delete $container"
                else
                    lxc delete -f "$container" 2>/dev/null || log_warn "Failed to delete $container"
                fi
            done
            log_success "Containers cleaned up"
        else
            log_info "No test containers found"
        fi
    else
        log_warn "LXD not installed, skipping container cleanup"
    fi
    
    echo ""
    
    # 2. Stop Gradle daemons if requested
    if [ "$STOP_GRADLE" = true ]; then
        log_info "Stopping Gradle daemons..."
        
        if command -v gradle &> /dev/null; then
            gradle --stop 2>/dev/null || log_warn "Failed to stop Gradle daemons"
            log_success "Gradle daemons stopped"
        elif [ -x "${SCRIPT_DIR}/gradlew" ]; then
            "${SCRIPT_DIR}/gradlew" --stop 2>/dev/null || log_warn "Failed to stop Gradle daemons"
            log_success "Gradle daemons stopped"
        else
            log_warn "Gradle not found, skipping daemon cleanup"
        fi
        
        # Also kill any lingering Gradle/Kotlin daemon processes
        log_info "Checking for lingering Gradle/Kotlin processes..."
        pkill -f "GradleDaemon" 2>/dev/null && log_info "  Killed Gradle daemons" || true
        pkill -f "KotlinCompileDaemon" 2>/dev/null && log_info "  Killed Kotlin daemons" || true
        
        echo ""
    fi
    
    # 3. Clean up test results if requested
    if [ "$CLEAN_RESULTS" = true ]; then
        log_info "Removing test results..."
        
        if [ -d "$RESULTS_DIR" ]; then
            rm -rf "$RESULTS_DIR"
            log_success "Test results directory removed: $RESULTS_DIR"
        else
            log_info "No test results directory found"
        fi
        
        echo ""
    fi
    
    # 4. Summary
    log_success "Cleanup completed!"
    
    # Show what's still running
    echo ""
    log_info "Current status:"
    
    if command -v lxc &> /dev/null; then
        local container_count
        if lxc list &> /dev/null; then
            container_count=$(lxc list --format=csv 2>/dev/null | grep "^${CONTAINER_PREFIX}-" | wc -l || echo "0")
        else
            container_count=$(sudo lxc list --format=csv 2>/dev/null | grep "^${CONTAINER_PREFIX}-" | wc -l || echo "0")
        fi
        echo "  - Test containers running: $container_count"
    fi
    
    local gradle_count=$(pgrep -f "GradleDaemon" | wc -l || echo "0")
    echo "  - Gradle daemons running: $gradle_count"
    
    local kotlin_count=$(pgrep -f "KotlinCompileDaemon" | wc -l || echo "0")
    echo "  - Kotlin daemons running: $kotlin_count"
    
    if [ -d "$RESULTS_DIR" ]; then
        echo "  - Test results directory: exists"
    else
        echo "  - Test results directory: removed"
    fi
    
    echo ""
}

# Run main
main

