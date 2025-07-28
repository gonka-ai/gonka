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
    **Key Changes:**
    - **Pre-Registration Setup**: Operator Key (cold) and AI Operational Key (hot) created before participant registration
    - **Permission Granting**: Operator Key grants authz permissions to AI Operational Key for all AI operations (`MsgStartInference`, `MsgFinishInference`, `MsgClaimRewards`, `MsgReportValidation`, etc.)
    - **Runtime Split**: 
      - AI Operations → AI Operational Key (automated, on-node via authz)
      - Admin Operations → Operator Key (manual, off-node)
    - **Participant Association**: NO direct association needed - AI Operational Key works via authz grants from Operator Key
    - **Address vs PubKey**: Only address needed for authz grants; AI Operational Key stored encrypted in node keyring 

- [DONE]: Create Full list of permission to be granted to Warm Key. INCLUDING AI OPERATION AND WHEN SEED CREATES NEW PARTICIPANT

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


- [WIP]: Create new inferenced command to:
    - Create and setup AI Operational Key: it should create it, fund from it's own balance with 1 nicoind and then grant all needed permission. It should be done with all best practises for such task

**Total: 5 manual authorization message types**

- [DONE]: Create a pre-init step when we:
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