INTRODUCTION
This document is our worksheet for MLNode proposal implementation 
NEVER delete this introduction

All tasks should be in format:
[STATUS]: Task
    Description

STATUS can be:
- [TODO]
- [WIP]
- [DONE]

You can work only at the task marked [WIP]. You need to solve this task in clear, simple and robust way and propose all solution minimalistic, simple, clear and concise

All tasks implementation should not break tests.

## Quick Start Examples

### 1. Build Project
```bash
make build-docker    # Build all Docker containers
make local-build     # Build binaries locally  
./local-test-net/stop.sh # Clean old containers
```

### 2. Run Tests
```bash
cd testermint && ./gradlew :test -DexcludeTags=unstable,exclude          # Stable tests only
cd testermint && ./gradlew :test --tests "TestClass" -DexcludeTags=unstable,exclude  # Specific class, stable only
cd testermint && ./gradlew :test --tests "TestClass.test method name"    # Specific test method
```

NEVER RUN MANY TESTERMINT TESTS AT ONCE

----
# MLNode Upgrade

## Overview

This proposal outlines a reliable, zero-downtime upgrade process for MLNode components across the network. While `inferenced` and `decentralized-api` have straightforward upgrade paths via Cosmovisor, MLNode requires coordinated network-wide upgrades due to consensus requirements and resource constraints.

## The Challenge

**Why MLNode Upgrades Are Complex:**
- **Container Size**: 10GB+ containers (CUDA + PyTorch + models) take minutes to pull/start
- **Lifecycle Requirement**: `.stop()` must be called on old version before new version can accept requests
- **Network Coordination**: All operators must upgrade simultaneously at `upgrade_height`
- **GPU Resources**: Limited memory prevents running duplicate inference workloads

## The Solution: Side-by-Side Deployment

**Architecture Overview:**
```
┌─────────────────┐    ┌──────────────┐    ┌─────────────────┐
│ decentralized-  │───▶│  ML Proxy    │───▶│ MLNode v3.0.6   │
│ api             │    │  (NGINX)     │    │ (old version)   │
└─────────────────┘    │              │    └─────────────────┘
                       │              │    ┌─────────────────┐
                       │              │───▶│ MLNode v3.0.8   │
                       └──────────────┘    │ (new version)   │
                                          └─────────────────┘
```

**How It Works:**
1. **Governance Proposal**: Sets `target_version` (e.g., `v3.0.8`) and `upgrade_height`
2. **Pre-Deployment**: Operators deploy new MLNode alongside old version
3. **Proxy Routing**: NGINX routes requests based on URL version paths
4. **Atomic Switch**: At `upgrade_height`, all API nodes switch to new version URLs
5. **Cleanup**: Old version receives `.stop()` call and is removed

**Benefits:**
- ✅ **Zero Downtime**: New version ready before switch
- ✅ **Atomic Network Switch**: All nodes switch simultaneously
- ✅ **Instant Rollback**: Change proxy routing back if issues arise
- ✅ **Resource Efficient**: Only one version active at a time

## What's Completed ✅

[DONE]: Core Version Management System
- **Chain-based Version Storage**: Added `MLNodeVersion` proto with `current_mlnode_version` field
- **Automatic Version Updates**: EndBlock updates version when upgrade height reached
- **Fallback Mechanism**: Nodes query chain if local version cache is empty or on restart
- **Exact Timing**: Nodes switch precisely at upgrade height via `ProcessNewBlockEvent()` on each block
- **No Chain Queries**: Uses known `NodeVersion` directly from upgrade plan data

[DONE]: URL Versioning Support  
- **Mock Server Enhancement**: Added version support to all mock servers for versioned routing
- **URL Patterns**: Support for `poc_port/VERSION/api/v1/...` and `inference_port/VERSION/v1/chat/...`
- **Call Site Updates**: All calls use `ConfigManager.GetCurrentNodeVersion()` with `InferenceUrl()`, `PoCURL()`
- **Default Version**: Set to `v3.0.8` for current deployments

[DONE]: Client Management & Persistence
- **Version Tracking**: Added `lastUsedVersion` field to config for detecting version changes
- **Automatic Client Refresh**: Periodic check (30s) refreshes MLNode clients when version changes
- **Lifecycle Management**: Old clients receive `.stop()` calls during version transitions
- **Thread Safety**: Mutex protection for MLNode client access via `GetClient()` method
- **Restart Safety**: Version persistence survives container restarts during upgrades

[DONE]: Architecture Improvements
- **Code Cleanup**: Removed ~200 lines of complex version stack management code
- **Separation of Concerns**: Clean split between height management and upgrade processing
- **Performance**: Eliminated unnecessary chain queries during normal operation
- **Startup Sync**: `SyncVersionFromChain()` catches up on missed upgrades after restart

## What's TODO 📋

[TODO]: Connection Pool with Auto-Healing
    **Why**: Current single gRPC connection architecture is fragile - one EOF error breaks all 30+ blockchain query systems
    **What**: Implement connection pool with auto-healing for cosmos client in `decentralized-api/cosmosclient/cosmosclient.go`
    - Create connection pool structure and configuration within InferenceCosmosClient to manage multiple cosmos SDK connections
    - Implement health check mechanism to monitor connection status and detect failed connections automatically  
    - Add auto-healing functionality to replace failed connections with new healthy ones in the background
    - Implement simple round-robin or random selection for distributing requests across healthy connections in the pool
    - Ensure graceful degradation when pool connections are reduced, with fallback to single connection mode
    **Key Implementation Advice**:
    - Add `pool []*cosmosclient.Client`, `mu sync.Mutex`, `next int` fields to InferenceCosmosClient struct
    - Use thread-safe round-robin selection: `next = (next + 1) % len(pool)` with mutex protection
    - Continue pool initialization even if some connections fail - graceful degradation is critical
    - Modify `NewInferenceQueryClient()` and `NewCometQueryClient()` to call `getClient()` from pool at client creation time (not per-request)
    - `getClient()` must verify connection health before returning - test with simple ping/status check
    - Add background goroutine with ticker (30s interval) for continuous health monitoring and connection replacement
    - CRITICAL: `getClient()` must try multiple connections if first is unhealthy - don't just return a failed connection
    - Keep existing method signatures unchanged to avoid breaking 30+ call sites across the application
    **Impact**: Eliminates single point of failure for blockchain queries, improves system resilience

[DONE]: Test Cleanup After Simplification
    **What**: Cleaned up broker tests after removing Version and AcceptEarlierVersion parameters
    - Removed obsolete TestVersionFiltering test (version filtering no longer exists)
    - Updated all LockAvailableNode calls to use simplified 2-parameter structure
    - Removed version-related node configuration from test setup
    - All broker tests now pass with simplified system
    
[DONE]: Integration Testing for Version Switching
    **Why**: Need comprehensive testing for upgrade scenarios to ensure reliability
    **What**: Write testermint test which test change version from v0.3.8 to v0.3.9 and to v0.3.10 and confirming that after each change - requests are going to new version for both api and inference
    **Where**: `testermint/` test framework and mock servers

    fun testVersionedEndpointSwitching() - now implementing incorrectly. it's registering new nodes, etc but defacto should just make schedule update and check after in wiremock somehow that for ALL future API and inference command new prefix was used. Do that 

[TODO]: Fix `create-partial-upgrade` to use --from as in `upgrade software-upgrade` command. Now it requires gov module addres?

## Node Operator Guide

### 1. Pre-Upgrade Setup

Deploy the new MLNode alongside your current version:

```bash
# Create shared network
docker network create gonka-net

# Run current version (stays running)
docker run -d --name mlnode-v306 --network gonka-net gonka/mlnode:3.0.6

# Deploy new version (ready but inactive)
docker run -d --name mlnode-v308 --network gonka-net gonka/mlnode:3.0.8
```

### 2. Configure Reverse Proxy

**nginx.conf:**
```nginx
events {}
http {
    upstream mlnode_v306 { server mlnode-v306:8000; }
    upstream mlnode_v308 { server mlnode-v308:8000; }
    
    server {
        listen 80;
        client_max_body_size 0;
        proxy_read_timeout 24h;
        
        # Versioned routes
        location /v3.0.6/ { proxy_pass http://mlnode_v306/; }
        location /v3.0.8/ { proxy_pass http://mlnode_v308/; }
        
        # Default route (backward compatibility)
        location / { proxy_pass http://mlnode_v306/; }
    }
}
```

```bash
# Deploy proxy
docker run -d --name ml-proxy -p 80:80 --network gonka-net \
  -v $(pwd)/nginx.conf:/etc/nginx/nginx.conf:ro nginx:alpine
```

### 3. Governance Vote

### Submit and Vote for proposal
```

### 4. Post-Upgrade Cleanup

After network stabilizes on new version:
```bash
# Remove old version
docker stop mlnode-v306 && docker rm mlnode-v306

# Update proxy to point directly to new version (optional)
```

## Technical Details

**URL Routing Patterns:**
- `/api/v1/*` → Current version (backward compatibility)
- `/v3.0.6/api/v1/*` → Old version (explicit)
- `/v3.0.8/api/v1/*` → New version (upgrade target)

**State Management:**
- Config persistence handles container restarts during upgrades
- Broker detects version changes and refreshes MLNode clients automatically
- Chain stores authoritative current version, local config provides caching

**Version Switching Flow:**
1. **Height Tracking**: Simple `SetHeight()` method tracks current block height on each received block
2. **Upgrade Detection**: `ProcessNewBlockEvent()` checks for upgrades on each new block height
3. **Version Switching**: Uses known `NodeVersion` from upgrade plan (no chain queries needed)
4. **Immediate Effect**: Config updated with new version, all new node connections use updated version
5. **Fallback Safety**: If API node was down, `GetCurrentNodeVersionWithFallback()` catches up on restart

## Alternative Deployments

- **Kubernetes**: Use Ingress resources for path-based routing
- **Cloud Platforms**: Use API Gateway services (AWS ALB, GCP Load Balancer)
- **Manual**: Any HTTP proxy supporting path-based routing

## Design Rationale

**Why Not Atomic Restart?**
- 2-5 minutes downtime per upgrade
- No coordination mechanism for decentralized network
- High risk if new version fails to start

**Why Not Rolling Updates?**
- Breaks consensus (different nodes on different versions)
- Complex rollback scenarios
- Network split risks

**Our Approach:**
- Zero downtime with instant rollback capability  
- Atomic network-wide switches at governance-defined heights
- Handles MLNode lifecycle constraints properly
- Resource efficient (only one version uses GPU)

---

## Connection Architecture Issue (For Future Discussion)

### The Problem
All blockchain queries share a **single gRPC connection**:

```go
func (icc *InferenceCosmosClient) NewInferenceQueryClient() types.QueryClient {
    return types.NewQueryClient(icc.Client.Context())  // Same connection always
}
```

### Why It's Problematic
**Single Point of Failure:** One EOF error breaks **all 30+ systems**:
- Training system, API endpoints, validation, broker, event processing
- All use `recorder.NewInferenceQueryClient()` → same underlying connection
- If connection corrupted → entire application can't query blockchain

### Example
```go
// Startup: EOF during version sync
queryClient := recorder.NewInferenceQueryClient() // Connection corrupted
config.SyncVersionFromChain(queryClient)          // EOF error

// Later: All systems broken
trainingClient := recorder.NewInferenceQueryClient() // Same corrupted connection  
apiClient := recorder.NewInferenceQueryClient()      // Same corrupted connection
validationClient := recorder.NewInferenceQueryClient() // Same corrupted connection
// All fail with connection errors
```

### Current Status
**Fixed by timing** - version sync moved to when blockchain is stable. But architecture remains fragile.

**Future Options:**
- Connection pool with auto-healing
- Fresh connection per critical operation  
- Retry wrapper around query clients

---

*For detailed implementation code, see the `decentralized-api/` directory. For test coverage, see `testermint/`.*