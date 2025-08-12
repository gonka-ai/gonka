# Local Test Network - Modular Docker Compose Setup

This directory contains a modular Docker Compose setup that allows you to mix and match different components based on your needs.

## File Structure

```
local-test-net/
├── docker-compose-base.yml      # Core services (chain-node, api, mock-server)
├── docker-compose.genesis.yml   # Genesis node specific settings
├── docker-compose.join.yml      # Join network specific settings  
├── docker-compose.explorer.yml  # Adds blockchain explorer
├── docker-compose.proxy.yml     # Adds reverse proxy
├── docker-compose.bridge.yml    # Adds Ethereum bridge service
├── docker-compose.tmkms.yml     # Adds TMKMS security layer
└── Makefile                     # Easy commands for common combinations
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

## Migration from Old Files

The old monolithic files are replaced by this modular system:

- `docker-compose-local.yml` → `base.yml + join.yml`
- `docker-compose-local-genesis.yml` → `base.yml + genesis.yml`  
- `docker-compose-local-genesis-with-explorer.yml` → `base.yml + genesis.yml + explorer.yml + proxy.yml`
- `docker-compose-local-tmkms.yml` → `base.yml + genesis.yml/join.yml + tmkms.yml`

You can now create any combination you need! 