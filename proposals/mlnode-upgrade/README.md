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

[WIP]: Testing Infrastructure
    **Why**: Need comprehensive testing for upgrade scenarios to ensure reliability
    **What**: 
    - Enhanced mock server with complete versioned routing support (COMPLETED)
    - All MLNode endpoints support version-specific responses (COMPLETED)
    - Basic versioned routing tests implemented (COMPLETED)
    - Implement full proxy simulation for end-to-end upgrade testing
    - Add advanced upgrade scenario tests (version switching, rollback, client refresh)
    **Where**: `testermint/` test framework and mock servers

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

Submit upgrade proposal:
```bash
inferenced tx inference partial-upgrade 12000 "v3.0.8" "" \
  --title "MLNode Performance Upgrade" \
  --summary "Critical improvements and bug fixes" \
  --deposit 10000nicoin \
  --from YOUR_KEY
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

*For detailed implementation code, see the `decentralized-api/` directory. For test coverage, see `testermint/`.*