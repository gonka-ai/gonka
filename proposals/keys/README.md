# Keys Management in Gonka Network

This document describes key management for the Gonka decentralized AI infrastructure.

## Key Types

### 1. Account Keys (Transaction Keys)
- **Purpose**: Transactions, account operations, governance
- **Algorithm**: SECP256K1  
- **Generation**: `inferenced keys add <key-name> --keyring-backend <backend>` (inference-chain/scripts/init-docker-genesis.sh:83)
- **Storage**: `~/.inference/` directory (shared via Docker volumes between chain/API nodes)
- **Backends**: `test` (plain text), `os` (system keyring), `file` (encrypted)
- **Access Control**: Filesystem permissions on keyring directory

### 2. Consensus Keys (Validator Keys)  
- **Purpose**: Block validation and consensus participation
- **Algorithm**: ED25519
- **Generation**: During `inferenced init` (inference-chain/scripts/init-docker-genesis.sh:43) or via TMKMS `tmkms softsign keygen` (tmkms/docker/init.sh:45)
- **Storage**: Local validator directory (`~/.inference/config/`) for genesis validators or TMKMS secure storage (`/root/.tmkms/secrets/`) for joining validators
- **Access Control**: File permissions or TMKMS secure connection
- **Security**: Double-signing prevention, hardware security module support

### 3. [Not used for now]: Worker Keys
- **Purpose**: ML node communication planned but unused
- **Algorithm**: ED25519  
- **Generation**: `CreateWorkerKey()` during participant registration (decentralized-api/apiconfig/config_manager.go:166)
- **Storage**: ⚠️ **PLAIN TEXT** in API node configuration files (`config.yaml`) - SECURITY RISK
- **Access Control**: File system permissions only

## Current Problems

**Single Account Key Controls Everything**: One SECP256K1 account key currently handles:
- AI operations (StartInference, SubmitPoC, ClaimRewards)  
- Governance voting (VoteOnProposal, ParameterChange)
- Fund transfers and treasury operations

**Security Risk**: Anyone with server access can vote or transfer funds without manual approval. The keyring directory (`~/.inference/`) is shared between chain and API nodes via Docker volume mounts, creating automated but potentially insecure control.

**Technical Issue**: Genesis validators use account-derived addresses while runtime validators use consensus-derived addresses, causing participant identification inconsistencies in statistics queries.

## Proposed Security Architecture

**Operational Key**
- **Purpose**: Automated AI transactions (StartInference, SubmitPoC, ClaimRewards)
- **Algorithm**: SECP256K1
- **Storage**: `~/.inference/operational/` directory (Docker volume mount)
- **Access Control**: Restricted to API service only
- **Backends**: `file` (encrypted) recommended

**Governance Key** 
- **Purpose**: Manual protocol voting and parameter changes
- **Algorithm**: SECP256K1
- **Storage**: External hardware wallet or secure `file` backend
- **Access Control**: Manual approval required, separate from automation
- **Backends**: Hardware wallet preferred, `file` with strong encryption

**Treasury Key (Optional - if skipped, Governance Key handles treasury operations)**
- **Purpose**: High-value fund transfers and delegation
- **Algorithm**: SECP256K1  
- **Storage**: Hardware wallet or multi-signature setup (each signer using hardware wallet)
- **Access Control**: Multiple approvals required
- **Backends**: Hardware wallet or multi-sig group with hardware wallets

### Multi-sig Groups (Advanced)
```
Company Participant:
├── Operational Key → Automated AI workloads
├── Governance Group → Multi-sig for protocol votes
│   ├── CEO/Founder
│   ├── CTO/Tech Lead  
│   └── Head of Operations
└── Treasury Group (Optional) → Separate multi-sig for high-value transfers
    ├── CEO/Founder
    ├── CFO/Finance Lead
    └── Board Member
```

Leverage existing x/group module for enterprise participants requiring multiple approvals from different organization members for governance and treasury operations.

## Security Management

### Current Security Issues
- **Worker keys stored in plain text**: Private keys written directly to `config.yaml` without encryption
- **Shared keyring access**: Same account key controls AI operations and governance voting
- **No separation of concerns**: Single key compromise affects all operations
- **No key rotation**: Keys remain static, increasing long-term compromise risk

### Implementation Priority
1. Separate operational and governance keys with different storage locations
2. Hardware wallet integration for all sensitive operations
3. [POST RELEASE]: Multi-signature governance groups using x/group module

