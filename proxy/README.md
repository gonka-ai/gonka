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
  - Modes: `issue` (default), `renew`, `renew-if-needed` (uses stored `order.id`; see Renewal)
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
| `SSL_CERT_SOURCE` | ./secrets/nginx-ssl | Host path bind-mounted at `/etc/nginx/ssl` |
| `PROXY_SSL_WAIT_SECONDS` | 60 | Max wait for `proxy-ssl` readiness during cert fetch |
| `NODE_ID` | proxy | Node identifier included in cert requests to `proxy-ssl` |
| `API_SERVICE_NAME` | api | Service name for API upstream |
| `NODE_SERVICE_NAME` | node | Service name for chain node upstreams |
| `EXPLORER_SERVICE_NAME` | explorer | Service name for explorer upstream |
| `KEY_NAME` | - | Optional stack key; when set, service names are prefixed as `<KEY_NAME>-*` |
| `RESOLVER` | 127.0.0.11 | DNS resolver for dynamic upstream resolution (override if needed) |
| `DISABLE_GONKA_API` | false | Set to `true` to disable `/api/v1/` and `/v1/` routes |
| `DISABLE_CHAIN_RPC` | false | Set to `true` to disable `/chain-rpc/` routes |
| `DISABLE_CHAIN_API` | false | Set to `true` to disable `/chain-api/` routes |
| `DISABLE_CHAIN_GRPC` | false | Set to `true` to disable `/chain-grpc/` routes |
| `CORS_ALLOW_ORIGIN` | * | Allowed Origin for CORS headers. Defaults to wildcard `*`. |
| `GLOBAL_RATE_LIMIT_RPS` | 1000 | Global "safety net" rate limit (default: 1000). |
| `GLOBAL_RATE_UNIT` | s | Unit for global limit (`s` or `m`). |
| `GLOBAL_BURST` | 5000 | Burst for global limit. |
| `GONKA_API_RATE_LIMIT_RPS` | 10 | Base rate for `/api/`. Combined with high burst for "Punisher" strategy. |
| `GONKA_API_RATE_UNIT` | m | Rate unit (`s` or `m`). Default `m` for slow recovery. |
| `GONKA_API_BURST` | 600 | High burst capacity allows spikes but penalizes sustained spam. |
| `GONKA_API_EXEMPT_ROUTES` | `chat inference training` | List of route prefixes to exempt. Matches prefix (e.g. `chat` matches `/chat`, `/chat/`, `/chat/123`). |
| `GONKA_API_BLOCKED_ROUTES` | `poc-batches` | List of route prefixes to BLOCK. Returns 403 Forbidden. |
| `CHAIN_API_EXEMPT_ROUTES` | - | List of Chain API route prefixes to exempt from standard limits. |
| `CHAIN_API_BLOCKED_ROUTES` | - | List of Chain API route prefixes to BLOCK. |
| `CHAIN_RPC_EXEMPT_ROUTES` | - | List of Chain RPC route prefixes to exempt from standard limits. |
| `CHAIN_RPC_BLOCKED_ROUTES` | - | List of Chain RPC route prefixes to BLOCK. |
| `CHAIN_GRPC_EXEMPT_ROUTES` | - | List of Chain gRPC route prefixes to exempt from standard limits. |
| `CHAIN_GRPC_BLOCKED_ROUTES` | - | List of Chain gRPC route prefixes to BLOCK. |
| `EXEMPT_RATE_LIMIT_RPS` | 500 | Rate limit for exempt routes. |
| `EXEMPT_RATE_UNIT` | s | Unit for exempt routes (`s` or `m`). |
| `EXEMPT_BURST` | 2000 | Burst for exempt routes. |
| `CHAIN_API_RATE_LIMIT_RPS` | 20 | Rate limit for `/chain-api/` (default: 20). |
| `CHAIN_API_RATE_UNIT` | m | Unit for chain API (`s` or `m`). Default `m`. |
| `CHAIN_API_BURST` | 200 | Burst for chain API. |
| `CHAIN_RPC_RATE_LIMIT_RPS` | 20 | Rate limit for `/chain-rpc/` (default: 20). |
| `CHAIN_RPC_RATE_UNIT` | m | Unit for chain RPC (`s` or `m`). Default `m`. |
| `CHAIN_RPC_BURST` | 200 | Burst for chain RPC. |
| `CHAIN_GRPC_RATE_LIMIT_RPS` | 20 | Rate limit for `/chain-grpc/` (default: 20). |
| `CHAIN_GRPC_RATE_UNIT` | m | Unit for chain gRPC (`s` or `m`). Default `m`. |
| `CHAIN_GRPC_BURST` | 200 | Burst for chain gRPC. |

> **Note**: `GLOBAL_RATE_LIMIT_RPS` acts as a total ceiling for a single IP. It must be higher than your highest specific limit (e.g. higher than Exempt limit).

### Modes

- `NGINX_MODE=http`: listen on 80 only; SSL disabled.
- `NGINX_MODE=https`: listen on 443 with SSL; requires `CERT_ISSUER_DOMAIN` and a reachable `proxy-ssl` service to obtain certs if missing.
- `NGINX_MODE=both`: listen on 80 and 443; same SSL requirements as `https`.

When SSL is enabled and no certs are present under `/etc/nginx/ssl`, `entrypoint.sh` will call `setup-ssl.sh` to fetch a certificate via the `proxy-ssl` service.

### Setup Environment

Below are minimal environment configurations for the compose stack under `deploy/join/config.env`. This section lists only environment variables; Docker commands are provided separately below.

#### HTTP only (80 → 8000)

```
NGINX_MODE=http
API_PORT=8000
```

#### HTTPS only via proxy-ssl (443 → 8443)

```
NGINX_MODE=https
API_SSL_PORT=8443
CERT_ISSUER_DOMAIN=your.domain
CERT_ISSUER_JWT_SECRET=change-me
ACME_ACCOUNT_EMAIL=you@example.com
ACME_DNS_PROVIDER=<route53|cloudflare|gcloud|azure|digitalocean|hetzner>
# Provider credentials per your DNS (see proxy-ssl README)
```

Notes:
- The compose maps ports 80 and 443; with `NGINX_MODE=https`, nginx listens on 443 only.
- Certificates are stored under `./secrets/nginx-ssl` (bind-mounted to `/etc/nginx/ssl`) and used automatically by `proxy`.

#### Both HTTP & HTTPS (80/443 → 8000/8443) via proxy-ssl

```
NGINX_MODE=both
API_PORT=8000
API_SSL_PORT=8443
CERT_ISSUER_DOMAIN=your.domain
CERT_ISSUER_JWT_SECRET=change-me
ACME_ACCOUNT_EMAIL=you@example.com
ACME_DNS_PROVIDER=<route53|cloudflare|gcloud|azure|digitalocean|hetzner>
# Provider credentials per your DNS (see proxy-ssl README)
```

#### HTTPS only with manual certs

Environment in `deploy/join/config.env`:

```
NGINX_MODE=https
API_SSL_PORT=8443
SERVER_NAME=your.domain
SSL_CERT_SOURCE=./secrets/nginx-ssl
# do not set CERT_ISSUER_DOMAIN when using manual certs
```

#### Both HTTP & HTTPS (80/443 → 8000/8443) (80 & 443) with manual certs

```
NGINX_MODE=both
API_PORT=8000
API_SSL_PORT=8443
SERVER_NAME=your.domain
SSL_CERT_SOURCE=./secrets/nginx-ssl
# do not set CERT_ISSUER_DOMAIN when using manual certs
```

#### Manual certificate issuance (Let’s Encrypt via Certbot DNS-01)

This works with any DNS provider using an interactive DNS-01 challenge. Certbot will pause and show a TXT record to add at `_acme-challenge.your.domain`. Create that record in your DNS, wait for propagation, then press Enter to continue.

Recommended (one-shot, writes directly into the mounted directory `deploy/join/secrets/nginx-ssl`):

- Host-installed Certbot:

```
DOMAIN=your.domain
ACCOUNT_EMAIL=your_email@example.com
mkdir -p secrets/nginx-ssl secrets/certbot/{config,work,logs}
sudo certbot certonly --manual --preferred-challenges dns \
  --config-dir ./secrets/certbot/config \
  --work-dir ./secrets/certbot/work \
  --logs-dir ./secrets/certbot/logs \
  -d "$DOMAIN" \
  --email "$ACCOUNT_EMAIL" --agree-tos --no-eff-email \
  --deploy-hook 'install -m 0644 "$RENEWED_LINEAGE/fullchain.pem" ./secrets/nginx-ssl/cert.pem; install -m 0600 "$RENEWED_LINEAGE/privkey.pem" ./secrets/nginx-ssl/private.key'
```

- Dockerized Certbot (no host install needed):

```
DOMAIN=your.domain
ACCOUNT_EMAIL=your_email@example.com
mkdir -p secrets/nginx-ssl secrets/certbot
docker run --rm -it \
  -v "$(pwd)/secrets/certbot:/etc/letsencrypt" \
  -v "$(pwd)/secrets/nginx-ssl:/mnt/nginx-ssl" \
  certbot/certbot certonly --manual --preferred-challenges dns \
  -d "$DOMAIN" --email "$ACCOUNT_EMAIL" --agree-tos --no-eff-email \
  --deploy-hook 'install -m 0644 "$RENEWED_LINEAGE/fullchain.pem" /mnt/nginx-ssl/cert.pem; install -m 0600 "$RENEWED_LINEAGE/privkey.pem" /mnt/nginx-ssl/private.key'
```

Renewal: rerun the same one-shot command before expiry (manual DNS step required each time), then reload nginx (see Docker commands below).

### Start with Docker Compose

Run from `deploy/join` after setting `config.env`.

- Prepare bind-mount directories (safe to rerun):

```
mkdir -p secrets/nginx-ssl secrets/certbot
```

- Initial start (enable HTTPS with proxy-ssl profile):

```
source ./config.env && \
docker compose --profile "ssl" -f docker-compose.yml -f docker-compose.mlnode.yml up -d
```

- Update currently running node:
  - With proxy-ssl (automated certs):

```
source ./config.env && \
docker compose --profile "ssl" -f docker-compose.yml -f docker-compose.mlnode.yml pull proxy proxy-ssl && \
docker compose --profile "ssl" -f docker-compose.yml -f docker-compose.mlnode.yml up -d proxy proxy-ssl
```

  - With manual certs (no proxy-ssl):

```
source ./config.env && \
docker compose -f docker-compose.yml -f docker-compose.mlnode.yml pull proxy && \
docker compose -f docker-compose.yml -f docker-compose.mlnode.yml up -d proxy
```

Notes:
- Ensure your env matches one of the setups above (proxy-ssl vs manual). See sections on environment configuration and manual certificate issuance.
- General operational guidance aligns with the Quickstart docs at [gonka.ai Host Quickstart](https://gonka.ai/host/quickstart/#how-to-stop-mlnode).

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