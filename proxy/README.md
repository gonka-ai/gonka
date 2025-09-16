# Nginx Reverse Proxy

This directory contains the nginx reverse proxy configuration that consolidates all services behind a single entry point.

## Overview

The nginx proxy routes requests to different backend services based on URL paths:

- `/api/v1/` → Main application API v1 (proxies to backend `/v1/`)
- `/v1/` → Direct API v1 (without `/api/` prefix)
- `/chain-rpc/` → Blockchain RPC endpoint (port 26657)
- `/chain-api/` → Blockchain REST API (port 1317)
- `/chain-grpc/` → Blockchain gRPC endpoint (port 9090)
- `/health` → Nginx health check endpoint
- `/` → Explorer dashboard when `DASHBOARD_PORT` is set, otherwise a simple "dashboard not configured" page

## Benefits

1. **Single Entry Point**: Only one port (80) needs to be exposed externally
2. **Simplified Networking**: No need to manage multiple port mappings
3. **Security**: Internal services are not directly accessible from outside
4. **Load Balancing**: Can easily add multiple backend instances
5. **SSL Termination**: Easy to add HTTPS support in one place
6. **Monitoring**: Centralized access logs and metrics
7. **Production Ready**: Standard architecture pattern for containerized apps

## Configuration Files

- `nginx.unified.conf.template` - Unified nginx configuration template rendered via env vars
- `entrypoint.sh` - Script that substitutes environment variables and starts nginx
- `setup-ssl.sh` - Helper to fetch TLS certs from `proxy-ssl` when HTTPS is enabled
- `Dockerfile` - Container image definition for the proxy service
- `README.md` - This documentation file

## Environment Variables

Key runtime environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `GONKA_API_PORT` | 9000 | Main application API port |
| `CHAIN_RPC_PORT` | 26657 | Blockchain RPC endpoint port |
| `CHAIN_API_PORT` | 1317 | Blockchain REST API port |
| `CHAIN_GRPC_PORT` | 9090 | Blockchain gRPC endpoint port |
| `DASHBOARD_PORT` | - | Explorer/Dashboard UI port; when set, `/` proxies to explorer |
| `NGINX_MODE` | http | One of `http`, `https`, or `both` (controls 80/443 and SSL) |
| `SERVER_NAME` | auto | Overrides nginx `server_name` (defaults to `CERT_ISSUER_DOMAIN` when SSL, else `localhost`) |
| `CERT_ISSUER_DOMAIN` | - | Required when `NGINX_MODE` is `https` or `both`; used for cert issuance and `server_name` |
| `PROXY_SSL_SERVICE_NAME` | proxy-ssl | Upstream service name for the cert issuer API |
| `PROXY_SSL_PORT` | 8080 | Port for the cert issuer API |
| `PROXY_SSL_WAIT_SECONDS` | 60 | Max wait for `proxy-ssl` readiness during cert fetch |
| `NODE_ID` | proxy | Node identifier included in cert requests to `proxy-ssl` |
| `API_SERVICE_NAME` | api | Service name for API upstream |
| `NODE_SERVICE_NAME` | node | Service name for chain node upstreams |
| `EXPLORER_SERVICE_NAME` | explorer | Service name for explorer upstream |
| `KEY_NAME` | - | Optional stack key; when set, service names are prefixed as `<KEY_NAME>-*` |
| `RESOLVER` | 127.0.0.11 | DNS resolver for dynamic upstream resolution (override if needed) |

### Modes

- `NGINX_MODE=http`: listen on 80 only; SSL disabled.
- `NGINX_MODE=https`: listen on 443 with SSL; requires `CERT_ISSUER_DOMAIN` and a reachable `proxy-ssl` service to obtain certs if missing.
- `NGINX_MODE=both`: listen on 80 and 443; same SSL requirements as `https`.

When SSL is enabled and no certs are present under `/etc/nginx/ssl`, `entrypoint.sh` will call `setup-ssl.sh` to fetch a certificate via the `proxy-ssl` service.

### Docker Compose

Below shows what to set in `deploy/join/config.env` and which commands to run. No compose YAML edits are required.

- HTTP only (no SSL):
  - Set in `deploy/join/config.env`:
    - `NGINX_MODE=http`
    - Ensure `CERT_ISSUER_DOMAIN` is unset
    - `API_PORT` (external port for HTTP, e.g., 8000)
  - Update only proxy:
    ```bash
    cd deploy/join
    source ./config.env
    docker compose pull proxy
    docker compose up -d proxy
    ```

- With SSL (443) via compose profile `ssl`:
  - Set in `deploy/join/config.env`:
    - `NGINX_MODE=https` (or `both` to keep 80 open)
    - `CERT_ISSUER_DOMAIN=<your.domain>`
    - `CERT_ISSUER_JWT_SECRET=<strong-secret>`
    - `API_SSL_PORT` (external port for HTTPS, e.g., 8443)
    - `ACME_ACCOUNT_EMAIL=<you@example.com>`
    - `ACME_DNS_PROVIDER=<route53|cloudflare|gcloud|azure|digitalocean|hetzner>`
    - Provider credentials per your DNS (see `proxy-ssl/README.md`)
    - Optional: `CERT_ISSUER_ALLOWED_SUBDOMAINS=explorer,api,rpc`
  - Update only proxy + proxy-ssl:
    ```bash
    cd deploy/join
    source ./config.env
    docker compose pull proxy proxy-ssl
    docker compose --profile ssl up -d proxy proxy-ssl
    ```

## Health Check

The proxy includes a health check endpoint at `/health` that returns HTTP 200 with "healthy" response.

## Troubleshooting

### TLS/SSL issues
1. Ensure `NGINX_MODE` is `https` or `both` and `CERT_ISSUER_DOMAIN` is set.
2. Verify `proxy-ssl` is running and reachable from `proxy` (see `proxy-ssl/README.md`).
3. Check logs of `proxy` for "SSL setup failed" or config validation errors.
4. Confirm DNS for `SERVER_NAME`/`CERT_ISSUER_DOMAIN` points to your proxy.

### Service Not Reachable
1. Check if the backend service is running: `docker compose ps`
2. Verify service names match the upstream definitions in nginx.conf
3. Check nginx logs: `docker compose logs nginx-proxy`

### WebSocket Issues
WebSocket support is configured for RPC connections and dashboard hot-reloading. If you have issues:
1. Verify the `Upgrade` and `Connection` headers are properly set
2. Check if the backend service supports WebSockets

### Performance Issues
1. Adjust `worker_connections` in nginx.conf
2. Enable additional caching if needed
3. Monitor nginx access logs for slow requests

## Security Features

- X-Frame-Options: DENY
- X-Content-Type-Options: nosniff  
- X-XSS-Protection: enabled
- Client body size limit: 10MB
- gzip compression enabled for better performance

## Migration from Static Ports

If you're upgrading from a previous version with hardcoded ports:

1. **Replace** `nginx.conf` with `nginx.unified.conf.template`
2. **Update** your Dockerfile to use the new entrypoint 
3. **Add** environment variables to your docker-compose.yml
4. **Rebuild** your nginx container

The entrypoint script provides sensible defaults, so existing setups will continue to work without changes. 