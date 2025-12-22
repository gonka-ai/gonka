# Local Test Network - Modular Docker Compose Setup

This directory contains a modular Docker Compose setup that allows you to mix and match different components based on your needs.

## File Structure

```
local-test-net/
├── docker-compose-base.yml           # Core services (chain-node, api, mock-server)
├── docker-compose.genesis.yml        # Genesis node specific settings
├── docker-compose.join.yml           # Join network specific settings  
├── docker-compose.explorer.yml       # Adds blockchain explorer
├── docker-compose.proxy.yml          # Adds reverse proxy
├── docker-compose.bridge.yml         # Adds Ethereum bridge service
├── docker-compose.tmkms.yml          # Adds TMKMS security layer
├── docker-compose.dns.yml            # Adds DNS server for wildcard ML-node hosts
├── docker-compose.dns-overrides.yml  # Configures services to use test DNS
├── dns/
│   └── Corefile                      # CoreDNS configuration
└── Makefile                          # Easy commands for common combinations
```

## Manual Usage

If you prefer to use `docker-compose` directly:

```bash
# Basic genesis
docker-compose -f docker-compose-base.yml -f docker-compose.genesis.yml up

# Join network with explorer
docker-compose -f docker-compose-base.yml -f docker-compose.join.yml -f docker-compose.explorer.yml up

# Any combination you want
docker-compose -f docker-compose-base.yml -f docker-compose.genesis.yml -f docker-compose.explorer.yml -f docker-compose.proxy.yml -f docker-compose.bridge.yml up
```

## Components

### Base (`docker-compose-base.yml`)
- **chain-node**: Blockchain node
- **api**: Decentralized API server  
- **mock-server**: Testing mock server

### Genesis Mode (`docker-compose.genesis.yml`)
- Sets `IS_GENESIS=true`
- Uses genesis initialization script
- Exposes additional ports (9090, 9091, 1317)

### Join Mode (`docker-compose.join.yml`) 
- Configures seed node connections
- Sets up network synchronization
- For joining existing networks

### Explorer Addon (`docker-compose.explorer.yml`)
- Adds blockchain explorer UI
- Configures API to connect to explorer
- Accessible at `http://explorer:5173`

### Proxy Addon (`docker-compose.proxy.yml`)
- Reverse proxy for unified access
- HTTP entry point on port 80 (redirects to HTTPS)
- HTTPS entry point on port 443 with automatic SSL certificate generation
- Health checks and dependency management
- Automatic SSL certificate generation on container startup

### Bridge Addon (`docker-compose.bridge.yml`)
- Ethereum bridge service for cross-chain operations
- Monitors Ethereum events and forwards to inference chain
- No external ports exposed (internal monitoring only)
- Geth + Prysm beacon chain for Ethereum connectivity

### TMKMS Addon (`docker-compose.tmkms.yml`)
- Adds Tendermint Key Management System for enhanced validator security
- Separates consensus key signing from the validator node
- Prevents double-signing attacks
- Uses secure key generation mode for new validators

### DNS Addon (`docker-compose.dns.yml` + `docker-compose.dns-overrides.yml`)
- Adds CoreDNS server for wildcard ML-node hostname resolution
- Enables configuring hundreds of ML-node hosts per stack without manual enumeration
- Resolves `ml-*.{KEY_NAME}.test` hostnames to the appropriate `{KEY_NAME}-mock-server`
- **Required files**: Both `docker-compose.dns.yml` and `docker-compose.dns-overrides.yml` must be included
- **Usage order**: Include `-f docker-compose.dns.yml -f docker-compose.dns-overrides.yml` after base file

#### ML-Node Hostname Patterns

For each node stack with `KEY_NAME`, you can use ML-node hostnames following this pattern:

- **For `KEY_NAME=genesis`**: `ml-0001.genesis.test`, `ml-0002.genesis.test`, ..., `ml-9999.genesis.test`
- **For `KEY_NAME=join1`**: `ml-0001.join1.test`, `ml-0002.join1.test`, ..., `ml-9999.join1.test`
- **For `KEY_NAME=join2`**: `ml-0001.join2.test`, `ml-0002.join2.test`, ..., `ml-9999.join2.test`
- **For `KEY_NAME=join3`**: `ml-0001.join3.test`, `ml-0002.join3.test`, ..., `ml-9999.join3.test`

All `ml-*.{KEY_NAME}.test` hostnames resolve to `{KEY_NAME}-mock-server` container.

#### Adding Support for Additional Stacks

To add DNS support for a new stack (e.g., `KEY_NAME=join4`), edit `local-test-net/dns/Corefile` and add:

```
join4.test:53 {
    log
    errors
    rewrite name regex ^ml-(.+)\.join4\.test$ join4-mock-server
    forward . /etc/resolv.conf
}
```

#### Example Usage

```bash
# Start genesis node with DNS support for multiple ML-node hosts
docker compose \
  -f docker-compose-base.yml \
  -f docker-compose.genesis.yml \
  -f docker-compose.dns.yml \
  -f docker-compose.dns-overrides.yml \
  up -d

# Configure decentralized-api with ML-node hosts in node_config.json:
# "ml-0001.genesis.test", "ml-0002.genesis.test", ..., "ml-0500.genesis.test"
```

## Environment Variables

Set these in your `.env` file or export them:

```bash
# Required
KEY_NAME=your-key-name
NODE_CONFIG=node-config.json

# Ports
PUBLIC_SERVER_PORT=9000
ML_SERVER_PORT=9100
ADMIN_SERVER_PORT=9200
ML_GRPC_SERVER_PORT=9300
WIREMOCK_PORT=8080
TMKMS_PORT=26658
PROXY_PORT=80      # HTTP proxy port
PROXY_HTTPS_PORT=443  # HTTPS proxy port

# For joining networks
SEED_NODE_RPC_URL=http://seed-node:26657
SEED_NODE_P2P_URL=seed-node:26656

# Optional
REST_API_ACTIVE=true  # Enable/disable REST API server
PROXY_ACTIVE=true     # Enable/disable proxy service
BRIDGE_ACTIVE=true    # Enable/disable bridge service
```

## Testing and Validation

### DNS Resolution Tests

After starting services with DNS support, you can validate that wildcard ML-node hostnames resolve correctly:

#### 1. Test ML-node hostname resolution

From inside the genesis-api container:
```bash
# Test that ml-*.genesis.test resolves to genesis-mock-server
docker exec genesis-api nslookup ml-0001.genesis.test
docker exec genesis-api nslookup ml-0500.genesis.test

# Verify it matches the mock-server IP
docker exec genesis-api nslookup genesis-mock-server
```

From inside the join1-api container:
```bash
# Test that ml-*.join1.test resolves to join1-mock-server
docker exec join1-api nslookup ml-0001.join1.test

# Verify it matches the mock-server IP
docker exec join1-api nslookup join1-mock-server
```

#### 2. Test existing container name resolution

Verify that existing container_name-based DNS lookups still work:
```bash
# Test container name resolution
docker exec genesis-api nslookup genesis-mock-server
docker exec genesis-api nslookup genesis-node
docker exec genesis-api nslookup test-dns
```

#### 3. Test DNS server functionality

Check that the CoreDNS server is running and accessible:
```bash
# View CoreDNS logs
docker logs test-dns

# Test DNS server directly
docker exec genesis-api dig @172.25.0.10 ml-0001.genesis.test
```

#### 4. Test with decentralized-api

Configure the `node_config.json` with ML-node hosts and verify communication:
```json
{
  "ml_nodes": [
    "http://ml-0001.genesis.test:8080",
    "http://ml-0002.genesis.test:8080",
    "http://ml-0003.genesis.test:8080"
  ]
}
```

All requests to these hosts should reach the `genesis-mock-server` container.

## Migration from Old Files

The old monolithic files are replaced by this modular system:

- `docker-compose-local.yml` → `base.yml + join.yml`
- `docker-compose-local-genesis.yml` → `base.yml + genesis.yml`  
- `docker-compose-local-genesis-with-explorer.yml` → `base.yml + genesis.yml + explorer.yml + proxy.yml`
- `docker-compose-local-tmkms.yml` → `base.yml + genesis.yml/join.yml + tmkms.yml`

You can now create any combination you need!