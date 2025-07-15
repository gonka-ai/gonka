# Nginx Reverse Proxy

This directory contains the nginx reverse proxy configuration that consolidates all services behind a single entry point.

## Overview

The nginx proxy routes requests to different backend services based on URL paths:

- `/api/` → Main application API (port 9000)
- `/chain-rpc/` → Blockchain RPC endpoint (port 26657)
- `/chain-api/` → Blockchain REST API (port 1317)
- `/chain-grpc/` → Blockchain gRPC endpoint (port 9090)
- `/dashboard/` → Explorer/Dashboard UI (port 5173)
- `/health` → Nginx health check endpoint
- `/` → Redirects to `/dashboard/`

## Benefits

1. **Single Entry Point**: Only one port (80) needs to be exposed externally
2. **Simplified Networking**: No need to manage multiple port mappings
3. **Security**: Internal services are not directly accessible from outside
4. **Load Balancing**: Can easily add multiple backend instances
5. **SSL Termination**: Easy to add HTTPS support in one place
6. **Monitoring**: Centralized access logs and metrics
7. **Production Ready**: Standard architecture pattern for containerized apps

## Configuration Files

- `nginx.conf.template` - Nginx configuration template with environment variable placeholders
- `entrypoint.sh` - Script that substitutes environment variables and starts nginx
- `Dockerfile` - Container image definition for the proxy service
- `README.md` - This documentation file

## Environment Variables

All backend service ports are configurable via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `API_PORT` | 9000 | Main application API port |
| `CHAIN_RPC_PORT` | 26657 | Blockchain RPC endpoint port |
| `CHAIN_API_PORT` | 1317 | Blockchain REST API port |
| `CHAIN_GRPC_PORT` | 9090 | Blockchain gRPC endpoint port |
| `DASHBOARD_PORT` | 5173 | Explorer/Dashboard UI port |

### Setting Environment Variables

**Option 1: In docker-compose.yml**
```yaml
nginx-proxy:
  environment:
    - API_PORT=8080
    - CHAIN_RPC_PORT=26658
    - DASHBOARD_PORT=3000
```

**Option 2: Using .env file**
```bash
# Create .env file in your project root
API_PORT=8080
CHAIN_RPC_PORT=26658
CHAIN_API_PORT=1318
CHAIN_GRPC_PORT=9091
DASHBOARD_PORT=3000
```

**Option 3: Export in shell**
```bash
export API_PORT=8080
export DASHBOARD_PORT=3000
docker compose up
```

## Usage in Docker Compose

See the example in `test-net-cloud/docker-compose-with-proxy.yml`:

```yaml
  nginx-proxy:
    container_name: nginx-proxy
    build:
      context: ../proxy
      dockerfile: Dockerfile
    ports:
      - "80:80"  # Only this port needs to be exposed externally
    environment:
      - API_PORT=${API_PORT:-9000}
      - CHAIN_RPC_PORT=${CHAIN_RPC_PORT:-26657}
      - CHAIN_API_PORT=${CHAIN_API_PORT:-1317}
      - CHAIN_GRPC_PORT=${CHAIN_GRPC_PORT:-9090}
      - DASHBOARD_PORT=${DASHBOARD_PORT:-5173}
    depends_on:
      - genesis-node
      - genesis-api
      - explorer
    networks:
      - chain-public
    restart: unless-stopped
```

Then remove external port mappings from other services (they'll only be accessible through nginx).

## Development vs Production

### Development
- Use `localhost:80/api/` instead of `localhost:9000/`
- Use `localhost:80/dashboard/` instead of `localhost:5173/`
- Use `localhost:80/chain-rpc/` instead of `localhost:26657/`

### Production
- Add SSL/TLS termination in nginx
- Use a proper domain name
- Add rate limiting and security headers (already included basic ones)
- Consider using nginx-proxy-manager for easier SSL certificate management

## Customization

### Adding New Services
1. Add upstream definition in `nginx.conf`:
   ```nginx
   upstream new_service_backend {
       server new-service:port;
   }
   ```

2. Add location block:
   ```nginx
   location /new-service/ {
       proxy_pass http://new_service_backend/;
       # ... standard proxy headers
   }
   ```

### SSL/HTTPS Setup
For production, add SSL configuration:

```nginx
server {
    listen 443 ssl http2;
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    # ... rest of configuration
}

server {
    listen 80;
    return 301 https://$server_name$request_uri;
}
```

## Health Check

The proxy includes a health check endpoint at `/health` that returns HTTP 200 with "healthy" response.

## Troubleshooting

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

1. **Replace** `nginx.conf` with `nginx.conf.template`
2. **Update** your Dockerfile to use the new entrypoint 
3. **Add** environment variables to your docker-compose.yml
4. **Rebuild** your nginx container

The entrypoint script provides sensible defaults, so existing setups will continue to work without changes. 