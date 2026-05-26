#!/bin/bash
# update-testnet-node.sh
# Testnet Node Updater & Cosmovisor Hot-Fixer.
# Usage: ssh user@host "bash -s" -- < update-testnet-node.sh <TAG> [--cosmovisor-only]

set -e

NEW_TAG=""
COSMOVISOR_ONLY=false

# 1. Parse Arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --tag) NEW_TAG="$2"; shift 2 ;;
    --cosmovisor-only) COSMOVISOR_ONLY=true; shift ;;
    *) 
       if [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9] ]] && [ -z "$NEW_TAG" ]; then
         NEW_TAG="$1"
       fi
       shift ;;
  esac
done

if [ -z "$NEW_TAG" ]; then
    echo ">>> Error: Script did not receive a version tag."
    exit 1
fi

# 2. Discovery Logic for Deployment Directory
DISCOVERED_DIR=$(docker inspect node --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' 2>/dev/null || echo "")
if [ -z "$DISCOVERED_DIR" ] || [ ! -d "$DISCOVERED_DIR" ]; then
    PATHS_TO_CHECK=("$HOME/gonka/deploy/join" "/srv/dai/gonka/deploy/join" "/srv/dai/deploy/join" "$TESTNET_BASE_DIR/deploy/join" "$TESTNET_BASE_DIR/gonka/deploy/join")
    for P in "${PATHS_TO_CHECK[@]}"; do
        if [ -d "$P" ]; then DISCOVERED_DIR="$P"; break; fi
    done
fi

if [ -z "$DISCOVERED_DIR" ]; then
    echo ">>> Error: Could not discover deploy directory on $(hostname)."
    exit 1
fi

cd "$DISCOVERED_DIR"
echo ">>> Working in $DISCOVERED_DIR"

# 3. Update docker-compose.yml (unless --cosmovisor-only)
if [ "$COSMOVISOR_ONLY" = false ]; then
    echo ">>> Step 1: Updating docker-compose.yml image tag to $NEW_TAG"
    cp docker-compose.yml docker-compose.yml.bak
    sed -i "/^  node:/,/^  [a-z]/ s|image: ghcr.io/product-science/inferenced:.*|image: ghcr.io/product-science/inferenced:$NEW_TAG|" docker-compose.yml
fi

# 4. Force-Inject binary into Cosmovisor folders
echo ">>> Step 2: Hot-fixing Cosmovisor binary from image $NEW_TAG..."

STATE_DIR=""
if [ -d ".inference" ]; then
    STATE_DIR=$(pwd)/.inference
elif [ -d "../../.inference" ]; then
    STATE_DIR=$(cd ../.. && pwd)/.inference
else
    STATE_DIR=$(docker inspect node --format '{{ range .Mounts }}{{ if eq .Destination "/root/.inference" }}{{ .Source }}{{ end }}{{ end }}' 2>/dev/null || echo "")
fi

if [ -n "$STATE_DIR" ] && [ -d "$STATE_DIR" ]; then
    COSMOVISOR_DIR="$STATE_DIR/cosmovisor"
    if [ -d "$COSMOVISOR_DIR" ]; then
        # Ensure we have the image
        docker pull "ghcr.io/product-science/inferenced:$NEW_TAG"

        # Helper to inject binary from image to a host path
        inject_bin() {
            local target_path="$1"
            echo ">>> Injecting binary into: $target_path"
            mkdir -p "$(dirname "$target_path")"
            rm -f "$target_path"  # Remove old file first to avoid 'Text file busy'
            docker run --rm "ghcr.io/product-science/inferenced:$NEW_TAG" cat /usr/bin/inferenced > "$target_path"
            chmod +x "$target_path"
        }

        # 1. Update Genesis
        inject_bin "$COSMOVISOR_DIR/genesis/bin/inferenced"

        # 2. Update all upgrade folders found
        if [ -d "$COSMOVISOR_DIR/upgrades" ]; then
            for upgrade_dir in "$COSMOVISOR_DIR/upgrades"/*; do
                if [ -d "$upgrade_dir/bin" ]; then
                    inject_bin "$upgrade_dir/bin/inferenced"
                fi
            done
        fi
        
        echo ">>> Cosmovisor hot-fix complete."
    else
        echo ">>> Warning: Cosmovisor directory not found in $STATE_DIR"
    fi
else
    echo ">>> Error: Could not find state directory (.inference) for node."
    exit 1
fi

# 5. Restart Container
echo ">>> Step 3: Restarting node container..."
COMPOSE_ARGS="-f docker-compose.yml"
[ -f "docker-compose.mlnode.yml" ] && COMPOSE_ARGS="$COMPOSE_ARGS -f docker-compose.mlnode.yml"

if [ -f "config.env" ]; then
    source config.env
    [ "$COSMOVISOR_ONLY" = false ] && docker compose $COMPOSE_ARGS pull node
    docker compose $COMPOSE_ARGS up -d --no-deps node
else
    [ "$COSMOVISOR_ONLY" = false ] && docker compose $COMPOSE_ARGS pull node
    docker compose $COMPOSE_ARGS up -d --no-deps node
fi

# 6. Final Verification
echo ">>> Step 4: Verifying running version..."
sleep 3
docker exec node cosmovisor run version --long 2>/dev/null | grep -E "commit|version" || echo ">>> Warning: Could not verify version yet (node still starting?)"

echo ">>> Success: Node update complete."
