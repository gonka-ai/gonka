# Proposal: MLNode Token-Based Authentication and FQDN Support

## Goal / Problem

Current MLNode registration in the API service requires three static parameters:
- Static IP address
- PoC port (management API, default 8080)
- Inference port (vLLM inference API, default 5000)

Both ports serve the same MLNode container through nginx proxy for version management (see `deploy/join/docker-compose.mlnode.yml` and `deploy/join/nginx.conf`).

Problems:
- Some cloud providers (e.g., Aliyun EAS) assign new IPs on container recreation, making static IP registration impractical
- Managing two ports adds operational complexity
- Cloud providers often assign stable FQDNs with token-based authentication (e.g., `http://<some-id>.ap-southeast-1.pai-eas.aliyuncs.com/api/predict/eas02/`) that remain consistent across deployments
- Current registration doesn't support using authentication tokens that managed services provide for access control

**Note:** Segment fields (InferenceSegment, PoCSegment) are legacy parameters that are always empty in current deployments.

## Proposal

Support additional registration method using full URLs:

1. Use single port (8080) since both endpoints proxy to the same container   
2. Allow registration using stable baseURLs with authentication tokens instead of IP/ports

The system will support both registration methods, allowing users to choose between IP/port configuration or baseURL-based registration

## Implementation

### Single-Port Operation

Current state (see `deploy/join/nginx.conf` for nginx setup and `mlnode/packages/api/src/api/proxy.py` for internal routing logic):

Management API (port 8080) supports:
- `http://<host>:<poc_port>/api/v1/*` - management API endpoints
- `http://<host>:<poc_port>/v1/*` - proxies to vLLM endpoints
- `http://<host>:<poc_port>/readyz` - ready for inference
- `http://<host>:<poc_port>/health` - whole service health, *not only inference*

Inference API (port 5000, backward compatible) supports:
- `http://<host>:<inference_port>/v1/*` - proxies to vLLM endpoints  
- `http://<host>:<inference_port>/health` - vLLM health check (proxied from vLLM backend)

`<poc_port>/health` checks whole service health while `<inference_port>/health` checks only vLLM backend health. API node currently checks MLNode health via `client.InferenceHealth()` at `http://<host>:<inference_port>/health`. New API binary must support both old MLNodes (port 5000) and new single-port configuration.

### Solution
Use registration method to determine which health endpoint to check:
- Legacy registration (Host/Port/Segment): Check `http://<host>:<inference_port>/health`
- New registration (baseURL): Check `<baseURL>/readyz` (management API readiness endpoint on port 8080)

### FQDN and Token Authentication

Current structure (`decentralized-api/apiconfig/config.go`):
```go
type InferenceNodeConfig struct {
    Host             string
    InferenceSegment string
    InferencePort    int
    PoCSegment       string
    PoCPort          int
    // ... other fields
}
```

Proposed structure for `InferenceNodeConfig` and `broker.Node`:
```go
type InferenceNodeConfig struct {
    // Existing fields (preserved for backward compatibility)
    Host             string
    InferenceSegment string  // Legacy, always empty
    InferencePort    int
    PoCSegment       string  // Legacy, always empty
    PoCPort          int
    
    // New optional fields (SQLite only, not stored on-chain)
    BaseURL          string  // Optional: full URL to MLNode (e.g., "http://service.provider.com/path/")
    AuthToken        string  // Optional: bearer token for authentication
    
    // ... other fields
}

type Node struct {
    // Existing fields
    Host             string
    InferenceSegment string
    InferencePort    int
    PoCSegment       string
    PoCPort          int
    
    // New optional fields
    BaseURL          string
    AuthToken        string
    
    // ... other fields
}
```

URL construction uses baseURL when present, otherwise falls back to `http://<host>:<port>/<segment>`. Version insertion for rolling upgrades works identically for both approaches: `<baseURL>/<version>/<path>` or `http://<host>:<port>/<version>/<path>`.

baseURL and AuthToken are stored in local SQLite database only, not on-chain. This allows each API node to configure its own MLNode access methods independently.

Required changes:

1. Add `base_url` and `auth_token` columns to SQLite schema with empty defaults for automatic migration
2. Update URL construction methods:
   - `broker.Node.InferenceUrlWithVersion()` and `broker.Node.PoCUrlWithVersion()` in `broker/broker.go` (main methods used for all inference and management calls including `/v1/chat/completions`)
   - Helper functions in `mlnode_background_manager.go` 
   - Helper functions in `setup_report.go` for consistency
3. Add `Authorization: Bearer <token>` header to all MLNode requests when AuthToken is set
4. Validate registration: require either (Host+Ports) OR baseURL, not both. baseURL must be valid HTTP(S) URL. AuthToken is always optional.


### Testing

1. Covered by unit tests and they pass:
- local `make local-build`
- [CICD](https://github.com/gonka-ai/gonka/actions/workflows/verify.yml)
2. Existing testermint tests pass:
- local `make build-docker && ./local-test-net/stop.sh &&  make run-tests`
- [Recommended] [CICD](https://github.com/gonka-ai/gonka/actions/workflows/integration.yml)
3. New node joins testnet and works with new MLNode registration

### Backward Compatibility

- Existing nodes using Host/Port configuration are unaffected
- baseURL and AuthToken are local SQLite configuration, not stored on-chain
- No migration needed - old and new registration methods coexist

