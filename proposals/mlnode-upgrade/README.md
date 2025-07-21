# MLNode Upgrade Proposal

## 1. The Challenge: Upgrading the MLNode

Our network relies on three core components: the `inferenced` chain node, the `decentralized-api` node, and the `MLNode` for AI workloads. While `inferenced` and `decentralized-api` have a straightforward upgrade path using Cosmovisor, the `MLNode` presents a unique challenge.

All `MLNode` instances must be upgraded simultaneously across the network to maintain consensus (we can't upgrade just binary because of tons of dependencies and different independent apps). A failed or inconsistent upgrade could disrupt the network. This proposal outlines a reliable, zero-downtime upgrade process for the `MLNode`.

## 2. The Solution: Side-by-Side Deployment

Our solution involves running the old and new `MLNode` versions side-by-side during a transition. A reverse proxy will manage traffic, routing requests to the correct version based on URL paths. The entire process is automated and triggered by an on-chain governance vote.

#### How It Works:

1.  **Governance Proposal:** A chain proposal sets a `target_version` (e.g., `v0.2.0`) and an `upgrade_height` (the block for the switch).
2.  **API Node Orchestration:** The `decentralized-api` node monitors the chain state.
    *   **Before `upgrade_height`:** It sends requests to the old version's path (e.g., `http://ml-proxy/v0.1.0/work`).
    *   **At `upgrade_height`:** It automatically switches all new requests to the new version's path (e.g., `http://ml-proxy/v0.2.0/work`).
3.  **Reverse Proxy Routing:** A proxy (like NGINX) acts as a stable entry point, forwarding versioned requests to the correct `MLNode` container.
    *   Requests to `/` go to the old `MLNode` container for backward compatibility.
    *   Requests to `/v0.1.0/*` go to the old `MLNode` container.
    *   Requests to `/v0.2.0/*` go to the new `MLNode` container.
4.  **MLNode Containers:** Both `MLNode` containers run concurrently, but only the active version processes workloads and uses the GPU.

## 3. Node Operator Upgrade Guide

This guide provides a standard implementation using Docker and NGINX. Any setup that can perform path-based routing is compatible.

### Step 1: Deploy the New MLNode and Proxy

Before the `upgrade_height` is reached, deploy the new `MLNode` container alongside the current one and configure your reverse proxy to handle both versions.

#### Example `nginx.conf`

This configuration supports both `v0.1.0` and `v0.2.0` simultaneously.

```nginx
events {}

http {
    # Define upstreams for each MLNode version
    upstream mlnode_v010 {
        server mlnode-010:8000;
    }

    upstream mlnode_v020 {
        server mlnode-020:8000;
    }

    server {
        listen 80;

        # --- SETTINGS FOR UNLIMITED SIZE & TIMEOUT ---
        client_max_body_size      0;      # No limit on request body size
        proxy_connect_timeout     24h;    # Long timeout for connection
        proxy_send_timeout        24h;    # Long timeout for sending data
        proxy_read_timeout        24h;    # Long timeout for receiving data

        # Route for the old version
        location /v0.1.0/ {
            proxy_pass http://mlnode_v010/;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }

        # Route for the new version
        location /v0.2.0/ {
            proxy_pass http://mlnode_v020/;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }

        # Default route for backward compatibility
        location / {
            proxy_pass http://mlnode_v010/;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }
    }
}
```

### Step 2: Deploy Containers with Docker

Use the following commands as a template.

```bash
# 1. Create a shared Docker network
docker network create gonka-net

# 2. Run the existing MLNode container
docker run -d --name mlnode-010 --network gonka-net gonka/mlnode:0.1.0

# 3. Run the new MLNode container
docker run -d --name mlnode-020 --network gonka-net gonka/mlnode:0.2.0

# 4. Run the NGINX proxy with your configuration
#    (assumes nginx.conf is in ./nginx)
docker run -d --name ml-proxy -p 80:80 \
  --network gonka-net \
  -v $(pwd)/nginx/nginx.conf:/etc/nginx/nginx.conf:ro \
  nginx:alpine
```

Your system is now ready for the on-chain upgrade. The `decentralized-api` will automatically switch traffic to the new version at the `upgrade_height`.

### Step 3: Cleanup After Upgrade

After the network has stabilized on the new version, you can safely stop and remove the old `MLNode` container and simplify your proxy configuration to conserve resources.

## 4. Alternative Environments

While this guide focuses on Docker and NGINX, the architecture is portable.

*   **Kubernetes:** Use an **Ingress** resource to manage path-based routing to different `Services` and `Deployments`.
*   **Cloud Platforms (AWS, GCP, etc.):** Use a native **API Gateway** service to route traffic. This is often simpler than managing a reverse proxy on a VM.



----
# TODO List

1. [DONE]: Inside deploy/inference write a docker-compose.yml to switch from 3.0.6 to 3.0.8. Version of nginx should be fixed
2. [DONE]: Find all places how we're using `nodeVersion`. I know that we don't update path to MLNode based on this

   **Key findings:**
   - ✅ Used for node selection/filtering in `broker.nodeAvailable()`
   - ✅ Used for upgrade scheduling via `NodeVersionStack`  
   - ✅ Managed by `ConfigManager.GetCurrentNodeVersion()`
   - ✅ **NOW IMPLEMENTED** - URL path modification for versioned routing:
   
   **Implementation completed:**
   - ✅ `Node.InferenceUrl(version)` - Supports versioned paths like `/v{version}/v1/chat/completions`
   - ✅ `Node.PoCUrl(version)` - Supports versioned paths like `/v{version}/api/v1/pow/*`
   - ✅ Updated all call sites to pass appropriate versions
   - ✅ Maintains backward compatibility when no version specified
   - ⏸️ Callback URLs - `/v1/poc-batches` paths - **Don't need to change for now**
   
   **Files modified:**
   - `decentralized-api/broker/broker.go` - URL methods and client creation
   - `decentralized-api/internal/server/public/post_chat_handler.go` - Chat completions and tokenization
   - `decentralized-api/internal/validation/inference_validation.go` - Validation requests

3. [DONE]: **Implementation Summary**
   
   Successfully implemented versioned URL support for MLNode upgrade mechanism. The implementation:
   
   - ✅ **Maintains backward compatibility** - Existing calls without version work unchanged
   - ✅ **Supports versioned routing** - New calls can specify version for `/v{version}/...` paths
   - ✅ **Handles both URL types** - Both `InferenceUrl()` and `PoCUrl()` methods support versioning
   - ✅ **Compiles and integrates cleanly** - No breaking changes to existing code
   
   **Example output:**
   ```
   InferenceUrl():        http://ml-proxy:8080
   InferenceUrl("3.0.6"): http://ml-proxy:8080/v3.0.6
   InferenceUrl("3.0.8"): http://ml-proxy:8080/v3.0.8
   ```
   
   This enables the reverse proxy (NGINX) to route requests to the correct MLNode container version during upgrades.

4. [WIP]: CLI command to propose update for new nodeVersion