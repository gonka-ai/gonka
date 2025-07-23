# Deploy Configuration

## Directory Structure

- `join/docker-compose.yml` - Network Node deployment
- `join/docker-compose.mlnode.yml` - MLNode deployment

## Upgrade Handling

**Network Node binaries** (`inferenced`, `decentralized-api`):
- Support on-chain upgrades via cosmovisor
- Binary replacement without container changes

**MLNode containers**:
- Need full container replacement during upgrades
- On-chain version switching stops work in old container and starts using new one
- Old and new containers run side-by-side during transition
- Only active version processes requests (saves resources)
- Old container removed after successful upgrade

## Nginx Proxy (deployed with MLNode)

Required in `docker-compose.mlnode.yml` for MLNode version switching:
- Routes requests to correct container based on URL path
- Enables atomic network-wide version switches
- Provides backward compatibility during transitions 