#!/bin/bash
set -e

# Golden Image Builder for Parallel Testing
# Creates a pre-configured LXD image with Docker, Java, and test dependencies

#=============================================================================
# Configuration
#=============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BASE_CONTAINER_NAME="gonka-base-$$"
IMAGE_NAME="gonka-test-runner"
TEMP_DIR="${SCRIPT_DIR}/.parallel-test-temp"

#=============================================================================
# Color output
#=============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

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
# Cleanup function
#=============================================================================

cleanup() {
    log_info "Cleaning up..."
    lxc delete -f "$BASE_CONTAINER_NAME" 2>/dev/null || true
}

trap cleanup EXIT

#=============================================================================
# Prerequisites check
#=============================================================================

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v lxc &> /dev/null; then
        log_error "LXD is not installed. Please install it first:"
        log_error "  sudo snap install lxd"
        log_error "  sudo lxd init --auto"
        exit 1
    fi
    
    if ! lxc list &> /dev/null; then
        log_error "LXD is not initialized. Please run: sudo lxd init --auto"
        exit 1
    fi
    
    if ! command -v docker &> /dev/null; then
        log_error "Docker is not installed on host. Please install Docker first."
        exit 1
    fi
    
    if ! command -v make &> /dev/null; then
        log_error "Make is not installed. Please install it first."
        exit 1
    fi
    
    log_success "All prerequisites satisfied"
}

#=============================================================================
# Build Docker images on host
#=============================================================================

build_docker_images() {
    log_info "Building Docker images on host..."
    
    cd "${WORKSPACE_DIR}"
    
    export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
    make build-docker
    
    log_success "Docker images built successfully"
}

#=============================================================================
# Save Docker images
#=============================================================================

save_docker_images() {
    log_info "Saving Docker images to tar files..."
    
    mkdir -p "${TEMP_DIR}/docker-images"
    cd "${TEMP_DIR}/docker-images"
    
    docker save -o inference-chain.tar ghcr.io/product-science/inferenced:latest
    docker save -o api.tar ghcr.io/product-science/api:latest
    docker save -o mock-server.tar inference-mock-server:latest
    docker save -o proxy.tar ghcr.io/product-science/proxy:latest
    
    log_success "Docker images saved to ${TEMP_DIR}/docker-images/"
}

#=============================================================================
# Create base container with Docker support
#=============================================================================

create_base_container() {
    log_info "Creating base container with Docker support..."
    
    # Launch base container with full nested Docker support
    lxc launch ubuntu:22.04 "$BASE_CONTAINER_NAME" \
        -c security.nesting=true \
        -c security.privileged=true \
        -c raw.lxc="lxc.apparmor.profile=unconfined" \
        -c linux.kernel_modules="overlay,br_netfilter,ip_tables,ip6_tables,iptable_nat,xt_conntrack"
    
    # Wait for container to be ready
    log_info "Waiting for container to be ready..."
    local max_wait=60
    local count=0
    while ! lxc exec "$BASE_CONTAINER_NAME" -- echo "ready" > /dev/null 2>&1; do
        sleep 1
        count=$((count + 1))
        if [ $count -ge $max_wait ]; then
            log_error "Container failed to start within ${max_wait}s"
            exit 1
        fi
    done
    
    log_success "Container ready after ${count}s"
    
    # Configure DNS
    log_info "Configuring DNS..."
    lxc exec "$BASE_CONTAINER_NAME" -- bash -c "rm -f /etc/resolv.conf && echo 'nameserver 8.8.8.8' > /etc/resolv.conf && echo 'nameserver 8.8.4.4' >> /etc/resolv.conf"
}

#=============================================================================
# Install dependencies
#=============================================================================

install_dependencies() {
    log_info "Installing dependencies (this may take a few minutes)..."
    
    lxc exec "$BASE_CONTAINER_NAME" -- bash -c "
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq
        apt-get install -y -qq \
            docker.io \
            docker-compose-v2 \
            openjdk-21-jdk \
            make \
            git \
            ca-certificates \
            curl
    "
    
    log_success "Dependencies installed"
}

#=============================================================================
# Pre-download Gradle and dependencies
#=============================================================================

setup_gradle() {
    log_info "Pre-downloading Gradle and test dependencies..."
    
    # Copy workspace temporarily to pre-cache Gradle
    lxc exec "$BASE_CONTAINER_NAME" -- mkdir -p /tmp/workspace
    
    cd "${WORKSPACE_DIR}"
    tar --exclude='.git' \
        --exclude='node_modules' \
        --exclude='target' \
        --exclude='build' \
        --exclude='.gradle' \
        --exclude='prod-local' \
        --exclude='.parallel-test-temp' \
        -czf - . | lxc exec "$BASE_CONTAINER_NAME" -- tar -xzf - -C /tmp/workspace
    
    # Run Gradle to download all dependencies
    lxc exec "$BASE_CONTAINER_NAME" -- bash -c "
        cd /tmp/workspace/testermint
        # Download Gradle wrapper and all dependencies
        ./gradlew testClasses --no-daemon > /dev/null 2>&1 || true
        # Cleanup workspace copy
        cd /
        rm -rf /tmp/workspace
    "
    
    log_success "Gradle and dependencies pre-cached in ~/.gradle"
    
    # Show Gradle cache size
    local cache_size=$(lxc exec "$BASE_CONTAINER_NAME" -- du -sh /root/.gradle 2>/dev/null | cut -f1 || echo "unknown")
    log_info "Gradle cache size: $cache_size"
}

#=============================================================================
# Start Docker and load images
#=============================================================================

setup_docker() {
    log_info "Starting Docker daemon..."
    
    lxc exec "$BASE_CONTAINER_NAME" -- bash -c "
        service docker start || dockerd > /dev/null 2>&1 &
        sleep 5
    "
    
    # Verify Docker is running
    if ! lxc exec "$BASE_CONTAINER_NAME" -- docker ps > /dev/null 2>&1; then
        log_error "Failed to start Docker daemon"
        exit 1
    fi
    
    log_success "Docker daemon started"
    
    log_info "Loading Docker images directly into Docker (not tar files)..."
    
    # Copy Docker images to container temporarily
    lxc exec "$BASE_CONTAINER_NAME" -- mkdir -p /tmp/docker-images
    lxc file push "${TEMP_DIR}/docker-images/"*.tar "$BASE_CONTAINER_NAME/tmp/docker-images/"
    
    # Load images into Docker (they'll be part of /var/lib/docker in the golden image)
    lxc exec "$BASE_CONTAINER_NAME" -- bash -c "
        docker load -i /tmp/docker-images/inference-chain.tar
        docker load -i /tmp/docker-images/api.tar
        docker load -i /tmp/docker-images/mock-server.tar
        docker load -i /tmp/docker-images/proxy.tar
        # Cleanup tar files - no longer needed
        rm -rf /tmp/docker-images
    "
    
    log_success "Docker images loaded into Docker daemon"
    
    # Verify Docker works by running a test container
    log_info "Verifying Docker functionality..."
    if lxc exec "$BASE_CONTAINER_NAME" -- docker run --rm alpine echo "Docker works" > /dev/null 2>&1; then
        log_success "Docker verification passed - containers work!"
    else
        log_error "Docker verification failed"
        exit 1
    fi
    
    # Show loaded images
    log_info "Loaded Docker images:"
    lxc exec "$BASE_CONTAINER_NAME" -- docker images --format "  {{.Repository}}:{{.Tag}} ({{.Size}})"
}

#=============================================================================
# Publish image
#=============================================================================

publish_image() {
    log_info "Stopping container to prepare for publishing..."
    
    # Stop Docker daemon cleanly
    lxc exec "$BASE_CONTAINER_NAME" -- bash -c "service docker stop || killall dockerd" 2>/dev/null || true
    sleep 2
    
    # Stop container
    lxc stop "$BASE_CONTAINER_NAME"
    
    log_info "Publishing container as image: $IMAGE_NAME"
    
    # Delete existing image if it exists
    if lxc image list | grep -q "$IMAGE_NAME"; then
        log_warn "Image $IMAGE_NAME already exists, deleting..."
        lxc image delete "$IMAGE_NAME"
    fi
    
    # Publish the container as an image (no compression for fast local usage)
    lxc publish "$BASE_CONTAINER_NAME" --alias "$IMAGE_NAME" \
        --compression=none \
        description="Gonka test runner with Docker, Java 21, and tar files of pre-loaded images"
    
    log_success "Image published: $IMAGE_NAME"
}

#=============================================================================
# Main execution
#=============================================================================

main() {
    echo ""
    log_info "Starting Golden Image Builder"
    log_info "This will create a pre-configured LXD container image for fast parallel testing"
    log_info "Note: Uses privileged containers with AppArmor unconfined for Docker support"
    echo ""
    
    # Create temp directory
    mkdir -p "${TEMP_DIR}"
    
    # Step 1: Check prerequisites
    check_prerequisites
    
    # Step 2: Build Docker images
    build_docker_images
    save_docker_images
    
    # Step 3: Create and configure base container
    create_base_container
    
    # Step 4: Install dependencies
    install_dependencies
    
    # Step 5: Setup Docker and load images
    setup_docker
    
    # Step 6: Pre-cache Gradle and dependencies
    setup_gradle
    
    # Step 7: Publish image
    publish_image
    
    # Cleanup temp files
    log_info "Cleaning up temporary files..."
    rm -rf "${TEMP_DIR}"
    
    echo ""
    log_success "Golden image '$IMAGE_NAME' created successfully!"
    echo ""
    log_info "You can now run parallel tests with:"
    log_info "  cd testermint && ./run-parallel-tests.sh"
    echo ""
    log_info "To rebuild this image in the future:"
    log_info "  lxc image delete $IMAGE_NAME"
    log_info "  cd testermint && ./setup-base-image.sh"
    echo ""
    
    # Show image info
    log_info "Image details:"
    lxc image info "$IMAGE_NAME"
}

# Run main
main

