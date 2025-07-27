INTRODUCTION
THAT DOCUMENTS DESCRIBES HOW NEW KEY MANAGEMENT WORKS AND WHAT USERFLOW WE HAVE
OUR GOAL TO MAINTAIN ALL DESCRIPTIONS CLEAR, SHORT, PRECISE CORRECT AND EASY TO UNDERSTAND
OUR SOLUTION IS BASED ON COSMOS-SDK AND WE'RE GOING TO MAINTAIN ALL BEST PRACTICES.

NEVER DELETE THIS INTRO

read project context in README.md
we maintain TODO list in proposals/keys/todo.md
we maintain high level desciption of state before this proposal and issues in proposals/keys/README.md
----

# Keys

We are implementing a role-based key management system. This architecture separates automated functions from high-stakes manual approvals, ensuring that no single key controls all network operations.

## [v0] Operator Key - Cold Wallet - MOST CRITICAL
- Purpose: Central point of control. It's address is used to store money
- Algorithm: SECP256K1
- Creation: Part of Account Creation
- Rotation: NO ROTATION
- Has to be `/group` as soon as possible 
- Granter: Grants permissions to the Governance, Treasury, and AI Operational keys using `authz`
- Signer for Validator Actions: Directly signs messages to create the validator and rotate its Consensus Key. Can also grant this rotation privilege to another key
- Who has: highest level stakeholder(s), must not be used directly except for granting

## [v1] Governance Key - Cold Wallet
- Purpose: Manual authorization of governance proposals and protocol parameter changes, can rotate 
- Algorithm: SECP256K1
- Creation: Created any time after Account Creation, privileges granted by Operator Key using /authz
- Rotation: Can be revoked or created any time using Operator Key
- Should be `/group`
- Who has: high level stakeholders

## [v1] Treasury Key - Cold Wallet
- Purpose: Used to store funds, authorizing high-value fund transfers, 
- Algorithm: SECP256K1
- Creation: Created separately and provided when participant is created
- Rotation: Can rotate any time using Operator Key
- Should be `/group`
- Who has: high level stakeholders

## [v0] AI Operational Key - Hot Wallet
- Purpose: Signing automated AI workload transactions (StartInference, SubmitPoC, ClaimRewards, etc.) 
- Algorithm: SECP256K1
- Storage: An encrypted file on the server, accessed programmatically by the `api` (and `node` ?) containers
- Creation: Created any time after Account Creation, privileges granted by Operator Key using /authz
- Rotation: Can be revoked or created any time using Operator Key


## [v0] Validator / Consensus / Tendermint Key - TMKSM with Secure Storage
- Purpose: Block validation and consensus participation
- Storage: Managed within a secure TMKMS service to prevent double-signing and protect the key.
- Algorithm: ED25519
- Creation: Created by TMKMS, provided on validator creation by Operator Key
- Rotation: Can be rotated with a message (`MsgRotateConsPubKey`) signed by the Operator Key (your Account Key) or one of its authorized grantee


## [Long Future] Maintainante Key
- Purpose: Rotate  Validator / Consensus / Tendermint Key
- Algorithm: SECP256K1
- Creation: Created any time after Account Creation, privileges granted by Operator Key using /authz
- Rotation: Can be revoked or created any time using Operator Key
- Should be `/group`

----

# Phase 0 / Launch

At the launch we have:

- **Operator Key** - Cold Wallet - used for Gov, Trease, Consensus Key rotation and AI Operational Key rotation 
- **AI Operational Key** - Hot Wallet - used for all AI related
- **Validator / Consensus / Tendermint Key** - TMKSM with Secure Storage

# UserFlow

## Join Node
1. Create Operator Key and Save Private Key Outside of server:
    - Q: How to achieve that private key never touches container? 

2. Create Key-Pair for AI Operational Key, grant permissions via `/grants`:
    - Q: Can it be done BEFORE node is registered? Or only after?

3. Create Validator Key via TMKMS, register as Validator using pub key:
    - Create inside TMKMS
    - `tmkms show-pubkey`
    - Run create via this key

    - Q: Can it be created only inside TMKMS? How to make UX comfortable? 

