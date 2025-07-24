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

You can work only at the task marked [WIP]. You need to sovle this task in clear, simple and robust way and propose all solution minimalistic, simple, clear and concise

All tasks implementation should not break tests.

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

## Implementation Status

[DONE]: Add version support to mock servers  
    Add version support into all mock servers to handle versioned routing for inference port and poc port
    we'll have:
    poc_port/VERSION/api/v1/....
    inference_port/VERSION/v1/chat/..
    inference_port/VERSION/tokenize
    inference_port/VERSION/health

[WIP]: Update call sites for version support
    Update all call sites to use `ConfigManager.GetCurrentNodeVersion()`, upgdate InferenceUrl(), PoCURL() accordingly to get versoin

[TODO]: Maintain proto field compatibility
    Don't change any proto fields - maintain existing proto structure

[TODO]: Version Change Detection & Persistence
    Add `PreviousNodeVersion` field to Config struct for restart safety
    Modify `SetHeight()` to capture version changes and persist state
    Create comprehensive unit tests covering all upgrade scenarios

[TODO]: Client Refresh System
    Add version tracking to Broker struct (`lastKnownVersion`)
    Implement automatic client refresh when version changes detected
    Add `.stop()` calls on old MLNode clients during version transitions
    Handle container restarts during upgrades

[TODO]: Testing Infrastructure
    Enhance mock server with versioned routing support
    Implement full proxy simulation for end-to-end upgrade testing
    Support all MLNode endpoints with version-specific responses

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

## Alternative Deployments

- **Kubernetes**: Use Ingress resources for path-based routing
- **Cloud Platforms**: Use API Gateway services (AWS ALB, GCP Load Balancer)
- **Manual**: Any HTTP proxy supporting path-based routing

## Technical Details

**URL Routing Patterns:**
- `/api/v1/*` → Current version (backward compatibility)
- `/v3.0.6/api/v1/*` → Old version (explicit)
- `/v3.0.8/api/v1/*` → New version (upgrade target)

**State Management:**
- Config persistence handles container restarts during upgrades
- `PreviousNodeVersion` tracking enables recovery from incomplete upgrades
- Broker detects version changes and refreshes MLNode clients

**Testing:**
- Mock server supports all versioned endpoints
- End-to-end upgrade scenarios testable without real containers
- Comprehensive test coverage for version switching logic

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

*For detailed implementation code and test coverage, see the `decentralized-api/` and `testermint/` directories.*