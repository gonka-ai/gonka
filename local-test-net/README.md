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
docker-compose -f docker-compose-base.yml -f docker-compose.genesis.yml -f docker-compose.explorer.yml -f docker-compose.proxy.yml up
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
- Single entry point on port 80
- Health checks and dependency management

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

# For joining networks
SEED_NODE_RPC_URL=http://seed-node:26657
SEED_NODE_P2P_URL=seed-node:26656

# Optional
WITH_EXPLORER=true  # Enable/disable explorer features
```

## Migration from Old Files

The old monolithic files are replaced by this modular system:

- `docker-compose-local.yml` → `base.yml + join.yml`
- `docker-compose-local-genesis.yml` → `base.yml + genesis.yml`  
- `docker-compose-local-genesis-with-explorer.yml` → `base.yml + genesis.yml + explorer.yml + proxy.yml`

You can now create any combination you need! 