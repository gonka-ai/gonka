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
cd testermint && ./gradlew :test --tests "TestClass" -DexcludeTags=unstable,exclude  # Specific class, stable only
cd testermint && ./gradlew :test --tests "TestClass.test method name"    # Specific test method
```

NEVER RUN MANY TESTERMINT TESTS AT ONCE

Current vision of final result is placed in `proposals/keys/flow.md`

Fully Ignore Worker Key for now 
Ignore genesis flow

IN code look only in inferene-chain and decentralized-api dirs
----

# Phase 0 / Launch

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

- [DONE]: Define how flow changes when AI Operational Key - Hot Wallet added
    **Defined key separation architecture**: Operator Key (cold) created offline for admin operations; AI Operational Key (hot) created on-server with authz permissions granted by Operator Key for automated AI operations. No direct participant association needed - AI Operational Key works via authz grants from Operator Key.

- [DONE]: Create Full list of permission to be granted to Warm Key. INCLUDING AI OPERATION AND WHEN SEED CREATES NEW PARTICIPANT
    **Created comprehensive permission list**: Documented 18 AI Operational Key message types (MsgStartInference, MsgFinishInference, MsgClaimRewards, etc.) and 5 future Governance Key message types. All permissions defined in `inference.InferenceOperationKeyPerms` array for automated ML operations.

## Full Permission List by Key Type

**Package:** `github.com/productscience/inference/x/inference/types`

### AI Operational Key (Automated Operations - Hot Wallet)
- `MsgStartInference` - Initiate AI inference requests
- `MsgFinishInference` - Complete AI inference execution  
- `MsgClaimRewards` - Automatically claim epoch rewards
- `MsgValidation` - Report validation results
- `MsgSubmitPocBatch` - Submit proof of compute batches
- `MsgSubmitPocValidation` - Submit PoC validation results
- `MsgSubmitSeed` - Submit randomness seed (seed nodes only)
- `MsgBridgeExchange` - Validate cross-chain bridge transactions
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

**Total: 18 automated message types**

### [Future] Governance Key (Manual Authorization - Cold Wallet)
- `MsgUpdateParams` - Governance parameter updates (authority only)
- `MsgRegisterModel` - Register new AI models (authority only)
- `MsgCreatePartialUpgrade` - System upgrades (authority only)
- `MsgSubmitUnitOfComputePriceProposal` - Propose compute pricing changes
- `MsgCreateTrainingTask` - Create new training tasks (operators/admins)

- [DONE]: Add command in inferenced CLI which register new participant with seed's `g.POST("participants", s.submitNewParticipantHandler)`
    **Implemented CLI participant registration**: Created `inferenced register-new-participant` command in `register_participant_command.go` that sends HTTP POST to seed node's `/v1/participants` endpoint. Command takes operator-address, node-url, operator-public-key, validator-consensus-key arguments and --node-address flag.

- [DONE]: Create new command received granted and grantee account and grants permissions. Code is in @permissions.go
    **Implemented permission granting CLI**: Created `inferenced tx inference grant-ml-ops-permissions` command in `module.go` that grants all 18 AI operation permissions from operator key to AI operational key using authz. Integrated with main CLI and supports standard transaction flags.

- [DONE]: Class to manage AccountKey and Operational Key
    **Built account management infrastructure**: Created `ApiAccount` struct in `accounts.go` with AccountKey/SignerAccount fields, implemented address methods, integrated keyring backend support, established `InferenceOperationKeyPerms` array, and added CLI integration for participant registration.

- [WIP]: Creating Account Key in API for tests
    **Implemented key creation in decentralized-api for test pipeline:**
    
    1. **Key Creation**: `decentralized-api/scripts/init-docker.sh` creates keys when `CREATE_KEY=true` using `inferenced keys add` with keyring-backend=test, keyring-dir=/root/.inference
    
    2. **Public Key Export**: Extracts ACCOUNT_PUBKEY via `inferenced keys show --pubkey` and exports as environment variable
    
    3. **Config Loading**: `decentralized-api/apiconfig/config_manager.go` requires both KEY_NAME and ACCOUNT_PUBKEY environment variables, loads into SignerKeyName and AccountPublicKey fields
    
    4. **Error Handling**: Script exits if CREATE_KEY=false and ACCOUNT_PUBKEY not provided, config loading fails if either env var missing
    
    **Usage**: Set `CREATE_KEY=true` for test nodes, `CREATE_KEY=false` with provided `ACCOUNT_PUBKEY` for production nodes

    5. [WIP] Make sure that approach works with api for genesis node
     **Implemented genesis key reuse for decentralized-api**: Modified `decentralized-api/scripts/init-docker.sh` to automatically extract `ACCOUNT_PUBKEY` from existing keys when neither `CREATE_KEY=true` nor `ACCOUNT_PUBKEY` is provided, with warning messages for production safety. Enhanced `decentralized-api/apiconfig/config_manager.go` to optionally use `ACCOUNT_PUBKEY` environment variable when provided. Genesis flow works in local-test-net: (1) `inference-chain/scripts/init-docker-genesis.sh` creates "genesis" key in shared `./prod-local/genesis:/root/.inference` volume, (2) decentralized-api detects existing "genesis" key and extracts public key with warnings, (3) both containers share keyring access for transaction signing. Volume sharing enables seamless key reuse in local development environments while maintaining backward compatibility.

**Total: 5 manual authorization message types**

- [TODO]: Create a pre-init step when we:
    - Create `Operator Key`
    - Create `AI Operational Key` (from server) and grant all needed permission to it from outside of server
    - Check that `AI Operational Key` has all this permissions granted
    **Implementation**: Minimal copy-pastable examples in `proposals/keys/minimal-example.md`


- [TODO]: Modify `docker-init.sh` to work with provided Public Key for `Operator Key` and Key Pair for `AI Operational Key`
    - Q: Which data structures should we modify minimally
- [TODO]: Make sure we can vote with `Operator Key` from outside of server
- [TODO]: Right testermint test for all this
- [TODO]: Figure out that all this works with ledger
- [TODO]: Key rotation 