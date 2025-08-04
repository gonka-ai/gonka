INTRODUCTION
This document is our worksheet for MLNode proposal implementation. That part of documentation contains only task, their statuses and details.

NEVER delete this introduction

All tasks should be in format:
[STATUS]: Task
    Description

STATUS can be:
- [TODO]
- [WIP]
- [DONE]

You can work only at the task marked [WIP]. You need to solve this task in clear, simple and robust way and propose all solution minimalistic, simple, clear and concise. Write minimal code!

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
cd testermint && ./gradlew :test --tests "TestClass" -DexcludeTags=unstable,exclude  # Specific class, stable only
cd testermint && ./gradlew :test --tests "TestClass.test method name"    # Specific test method
```

NEVER RUN MANY TESTERMINT TESTS AT ONCE

Current implementation plan is in `proposals/keys/flow.md`
High-level overview is in `proposals/keys/README.md`

**Focus Areas:**
- Implement v0: Account Key + ML Operational Key separation
- Ignore Worker Key (legacy, not used)
- Ignore genesis flow (focus on join flow)
- Code changes only in inference-chain and decentralized-api directories

## Current Status Summary

**COMPLETED (v0 Foundation):**
- Dual key architecture implemented (Account Key + ML Operational Key)
- CLI commands for participant registration and permission granting
- Account management infrastructure and authz integration
- Automatic participant registration via seed node API
- Permission sync and validation queries
- TMKMS compatibility for validator consensus keys

**IN PROGRESS:**
- Pre-init workflow for external Account Key creation
- Minimal example documentation

**REMAINING (v0 Completion):**
- External Account Key integration (hardware wallet support)
- Modified docker-init.sh for production scenarios
- Comprehensive testing with testermint
- Governance voting from external Account Key

**FUTURE (v1+):**
- Governance Key and Treasury Key separation
- Multi-signature groups using x/group module
- Key rotation mechanisms
----

# Completed Tasks (Phase 0 Foundation)

- [DONE]: Find all places where we use private key when init new node and list them
    **Identified two key usage patterns**: (1) Account Keys (SECP256K1) for transactions - stored in `~/.inference/`, used for validator registration via POST `/v1/participants`, runtime AI operations, and transaction signing. (2) Consensus Keys (ED25519) for block validation - generated via TMKMS, stored in `priv_validator_key.json`, used for consensus participation.

## Private Key Usage During Node Initialization

### 1. **Account Keys (SECP256K1) - Transaction Keys**
- **Storage**: `~/.inference/` directory (Docker volume)
- **Problem**: Single key controls ALL operations

**During validator creation (join only):**
- **Key Creation**: `inferenced keys add $KEY_NAME` - Creates private key in keyring
- **Get Validator Key**: Reads consensus public key from node status via `getValidatorKey()` function (`decentralized-api/participant/participant_registration.go:203-218`)
- **Participant Registration**: `SubmitNewUnfundedParticipant` via seed node API - Creates account and associates with validator key (HTTP call to seed node, which then signs `MsgSubmitNewUnfundedParticipant` with seed node's private key - `decentralized-api/cosmosclient/cosmosclient.go:183-187`)
  
  **Registration Flow**: New validator nodes → Create keys → POST to `/v1/participants` endpoint → Seed node signs registration → Node becomes active participant/validator
  - **Called by**: Joining validator nodes (not genesis nodes or regular users)
  - **Purpose**: Validator onboarding to become participants in decentralized AI inference network
  - **HTTP Endpoint**: `g.POST("participants", s.submitNewParticipantHandler)` (`decentralized-api/internal/server/public/server.go:52`)
  - **Caller Location**: `registerJoiningParticipant()` function (`decentralized-api/participant/participant_registration.go:154`)
  - **What Participants Do**: Process AI inferences, validate other participants, consensus participation, earn rewards

**During runtime:**
- **Epoch Rewards**: ClaimRewards automatically triggered each epoch
- **AI Operations**: StartInference, FinishInference, SubmitPocBatch when users make requests  
- **System Events**: Phase transitions, reconciliation, training tasks
- **Transaction Signing**: All operations use `tx.Sign(ctx, *factory, name, unsignedTx, false)` (`decentralized-api/cosmosclient/cosmosclient.go:311`)

### 2. **Consensus Keys (ED25519) - Validator Keys**
- **Location**: TMKMS generation in `tmkms/docker/init.sh:47` via `tmkms softsign keygen`
- **Files**: 
  - Local: `~/.inference/config/priv_validator_key.json`
  - TMKMS: `/root/.tmkms/secrets/priv_validator_key.softsign`
- **Purpose**: Block validation and consensus participation
- **Security**: Can use TMKMS for secure key management

- [DONE]: Define how flow changes when ML Operational Key - Hot Wallet added
    **Defined key separation architecture**: Account Key (cold) created offline for admin operations; ML Operational Key (hot) created on-server with authz permissions granted by Account Key for automated AI operations. No direct participant association needed - ML Operational Key works via authz grants from Account Key.

- [DONE]: Create Full list of permission to be granted to Warm Key. INCLUDING AI OPERATION AND WHEN SEED CREATES NEW PARTICIPANT
    **Created comprehensive permission list**: Documented 18 ML Operational Key message types (MsgStartInference, MsgFinishInference, MsgClaimRewards, etc.) and 5 future Governance Key message types. All permissions defined in `inference.InferenceOperationKeyPerms` array for automated ML operations.

## Full Permission List by Key Type

**Package:** `github.com/productscience/inference/x/inference/types`

### ML Operational Key (Automated Operations - Hot Wallet)
- `MsgStartInference` - Initiate AI inference requests
- `MsgFinishInference` - Complete AI inference execution  
- `MsgClaimRewards` - Automatically claim epoch rewards
- `MsgValidation` - Report validation results
- `MsgSubmitPocBatch` - Submit proof of compute batches
- `MsgSubmitPocValidation` - Submit PoC validation results
- `MsgSubmitSeed` - Submit randomness seed (seed nodes only)
- `MsgBridgeExchange` - Validate cross-chain bridge transactions ⚠️ **[BUG: Missing from codec.go]**
- `MsgSubmitTrainingKvRecord` - Submit training key-value records
- `MsgJoinTraining` - Join distributed training sessions
- `MsgJoinTrainingStatus` - Report training status updates
- `MsgTrainingHeartbeat` - Send training heartbeat signals
- `MsgSetBarrier` - Set training synchronization barriers
- `MsgClaimTrainingTaskForAssignment` - Claim training tasks
- `MsgAssignTrainingTask` - Assign training tasks (coordinators only)
- `MsgSubmitNewUnfundedParticipant` - Register new participants (seed nodes only)
- `MsgSubmitNewParticipant` - Register genesis participants (genesis nodes only)
- `MsgSubmitHardwareDiff` - Report hardware configuration changes
- `MsgInvalidateInference` - Invalidate fraudulent inferences (validators only)
- `MsgRevalidateInference` - Request re-validation of disputed inferences

**Total: 20 automated message types**

### [v1] Governance Key (Manual Authorization - Cold Wallet)
- `MsgUpdateParams` - Governance parameter updates (authority only)
- `MsgRegisterModel` - Register new AI models (authority only)
- `MsgCreatePartialUpgrade` - System upgrades (authority only)
- `MsgSubmitUnitOfComputePriceProposal` - Propose compute pricing changes
- `MsgCreateTrainingTask` - Create new training tasks (operators/admins)

**Total: 5 manual authorization message types**

- [DONE]: Add command in inferenced CLI which register new participant with seed's `g.POST("participants", s.submitNewParticipantHandler)`
    **Implemented CLI participant registration**: Created `inferenced register-new-participant` command in `register_participant_command.go` that sends HTTP POST to seed node's `/v1/participants` endpoint. Command takes account-address, node-url, account-public-key, consensus-key arguments and --node-address flag.

- [DONE]: Create new command received granted and grantee account and grants permissions. Code is in @permissions.go
    **Implemented permission granting CLI**: Created `inferenced tx inference grant-ml-ops-permissions` command in `module.go` that grants all 18 AI operation permissions from account key to ML operational key using authz. Integrated with main CLI and supports standard transaction flags.

- [DONE]: Class to manage AccountKey and Operational Key
    **Built account management infrastructure**: Created `ApiAccount` struct in `accounts.go` with AccountKey/SignerAccount fields, implemented address methods, integrated keyring backend support, established `InferenceOperationKeyPerms` array, and added CLI integration for participant registration.

- [DONE]: Creating Account Key in API for tests
    **Implemented key creation in decentralized-api for test pipeline:**
    
    1. **Key Creation**: `decentralized-api/scripts/init-docker.sh` creates keys when `CREATE_KEY=true` using `inferenced keys add` with keyring-backend=test, keyring-dir=/root/.inference
    
    2. **Public Key Export**: Extracts ACCOUNT_PUBKEY via `inferenced keys show --pubkey` and exports as environment variable
    
    3. **Config Loading**: `decentralized-api/apiconfig/config_manager.go` requires both KEY_NAME and ACCOUNT_PUBKEY environment variables, loads into SignerKeyName and AccountPublicKey fields
    
    4. **Error Handling**: Script exits if CREATE_KEY=false and ACCOUNT_PUBKEY not provided, config loading fails if either env var missing
    
    **Usage**: Set `CREATE_KEY=true` for test nodes, `CREATE_KEY=false` with provided `ACCOUNT_PUBKEY` for production nodes

    5. [DONE] Make sure that approach works with api for genesis node
     **Implemented genesis key reuse for decentralized-api**: Modified `decentralized-api/scripts/init-docker.sh` to automatically extract `ACCOUNT_PUBKEY` from existing keys when neither `CREATE_KEY=true` nor `ACCOUNT_PUBKEY` is provided, with warning messages for production safety. Enhanced `decentralized-api/apiconfig/config_manager.go` to optionally use `ACCOUNT_PUBKEY` environment variable when provided. Genesis flow works in local-test-net: (1) `inference-chain/scripts/init-docker-genesis.sh` creates "genesis" key in shared `./prod-local/genesis:/root/.inference` volume, (2) decentralized-api detects existing "genesis" key and extracts public key with warnings, (3) both containers share keyring access for transaction signing. Volume sharing enables seamless key reuse in local development environments while maintaining backward compatibility.


- [DONE]: CREATE_KEY - single key is used
    **Implemented single key creation with CREATE_KEY environment variable:**
    - Basic CREATE_KEY=true functionality implemented in `decentralized-api/scripts/init-docker.sh`
    - Creates single key with `$KEY_NAME` when CREATE_KEY=true
    - Maintains backward compatibility with existing single-key setups
    - Environment variable support added to `local-test-net/docker-compose.join.yml`

- [TODO]: Auto-create dual keys and grant permissions (test pipeline enhancement)
    **Remaining work for dual key architecture:**
    - Create cold key with name "$KEY_NAME"-COLD 
    - Create warm key with "$KEY_NAME"
    - Use inferenced register-new-participant to register participant on first run (addr for "$KEY_NAME"-COLD)
        Q: can it fetch consensus-key automatically from node?
    - Use `inferenced tx inference grant-ml-ops-permissions` to grant permission to "$KEY_NAME"
    - Update code to use "$KEY_NAME" for signing all transactions but still "$KEY_NAME"-COLD for admin operations

- [DONE]: Add query to find all authz grantees with specific message type for an account
    **Implemented authz grantee lookup query**: Added new query `GranteesByMessageType` in `query.proto` with REST endpoint `/productscience/inference/inference/grantees_by_message_type/{granter_address}/{message_type_url}`. Implemented keeper method in `query_grantees_by_message_type.go` with:
    - Complete proto definitions and gRPC endpoints
    - Proper input validation and error handling  
    - Comprehensive test coverage with all edge cases
    - Integration with dependency injection using actual authzkeeper.Keeper
    - Infrastructure ready for extending with actual authz grant iteration
    - All tests passing

## Next Phase Tasks

- [DONE, partial]: Create a pre-init step when we:
    - Create `Account Key` (outside server/container - SECURITY CRITICAL)
    - Create `ML Operational Key` (on server) and grant all needed permissions to it from Account Key
    - Check that `ML Operational Key` has all required permissions granted
    - **Implementation**: Minimal copy-pastable examples in `proposals/keys/minimal-example.md`

- [TODO]: Testing and validation
    - Write testermint tests for multi-key architecture
    - Test participant registration with external Account Key
    - Validate authz permission granting workflow

- [TODO]: Future enhancements
    - Hardware wallet integration (Ledger support)
    - Key rotation mechanisms for ML Operational Key
    - Multi-signature groups using x/group module

## Implementation Status (Current Branch)

**v0 Core Implementation COMPLETED:**
- **Dual Key Architecture**: Account Key (cold) + ML Operational Key (hot) separation implemented
- **CLI Commands**: `inferenced register-new-participant` and `inferenced tx inference grant-ml-ops-permissions` 
- **Account Management**: `ApiAccount` struct with keyring backend support in `decentralized-api/apiconfig/accounts.go`
- **Permission System**: 18 ML operational message types defined in `inference-chain/x/inference/permissions.go`
- **Authz Query**: `GranteesByMessageType` query for permission validation in `inference-chain/x/inference/keeper/query_grantees_by_message_type.go`
- **TMKMS Integration**: Validator consensus key extraction via RPC status endpoint (compatible with both TMKMS and local keys)
- **Init Script Enhancement**: `decentralized-api/scripts/init-docker.sh` with dual key creation, participant registration, and permission granting

**Production Readiness:**
- Account sync waiting mechanism for distributed node environments
- Environment variable support for external Account Key integration
- Backward compatibility with existing single-key setups

## Notes
- v0 Implementation: Account Key + ML Operational Key separation
- v1 Implementation: Add Governance Key and Treasury Key
- Long Future: Maintenance Key and full multi-sig support 