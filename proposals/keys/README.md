# Keys Management in Gonka Network

This document describes key management for the Gonka decentralized AI infrastructure.

We are implementing a role-based key management system. This architecture separates automated functions from high-stakes manual approvals, ensuring that no single key controls all network operations.

## Key Types

### [v0] Account Key - Cold Wallet - MOST CRITICAL
- **Purpose**: Central point of control
- **Algorithm**: SECP256K1
- **Creation**: Part of Account Creation
- **Has to be**: `/group` as soon as possible 
- **Granter**: Grants permissions to the Governance, Treasury, and ML Operational keys using `authz`
- **Signer for Validator Actions**: Directly signs messages to create the validator and rotate its Consensus Key. Can also grant this rotation privilege to another key
- **Who has**: Highest level stakeholder(s), must not be used directly except for granting

### [v1] Governance Key - Cold Wallet
- **Purpose**: Manual authorization of governance proposals and protocol parameter changes
- **Algorithm**: SECP256K1
- **Creation**: Created any time after Account Creation, privileges granted by Account Key using `/authz`
- **Rotation**: Can be revoked or created any time using Account Key
- **Should be**: `/group`
- **Who has**: High level stakeholders

### [v1] Treasury Key - Cold Wallet
- **Purpose**: Used to store funds, authorizing high-value fund transfers
- **Algorithm**: SECP256K1
- **Creation**: Created separately and provided when participant is created
- **Rotation**: Can rotate any time using Account Key
- **Should be**: `/group`
- **Who has**: High level stakeholders

### [v0] ML Operational Key - Warm Wallet
- **Purpose**: Signing automated AI workload transactions (StartInference, SubmitPoC, ClaimRewards, etc.)
- **Algorithm**: SECP256K1
- **Storage**: An encrypted file on the server, accessed programmatically by the `api` (and `node` ?) containers
- **Creation**: Created any time after Account Creation, privileges granted by Account Key using `/authz`
- **Rotation**: Can be revoked or created any time using Account Key

### [v0] Consensus (also called Validator or Tendermint Key) - TMKMS with Secure Storage
- **Purpose**: Block validation and consensus participation
- **Storage**: Managed within a secure TMKMS service to prevent double-signing and protect the key
- **Algorithm**: ED25519
- **Creation**: Created by TMKMS, provided on validator creation by Account Key
- **Rotation**: Can be rotated with a message (`MsgRotateConsPubKey`) signed by the Account Key or one of its authorized grantees

### [Long Future] Maintenance Key
- **Purpose**: Rotate Consensus Key
- **Algorithm**: SECP256K1
- **Creation**: Created any time after Account Creation, privileges granted by Account Key using `/authz`
- **Rotation**: Can be revoked or created any time using Account Key
- **Should be**: `/group`

## Phase 0 / Launch

At the launch we have:
- **Account Key** - Cold Wallet - used for Gov, Treasury, Consensus Key rotation and ML Operational Key rotation 
- **ML Operational Key** - Warm Wallet - used for all AI related transactions
- **Consensus Key** - TMKMS with Secure Storage


## Implementation Priority
1. [v0] Separate account and ML operational keys with different storage locations
2. [v1] Hardware wallet integration for governance and treasury operations
3. [Long Future] Multi-signature governance groups using x/group module


----

##  [v1] Multi-sig Groups (Advanced)
```
Company Participant:
├── Account Key → Secure Storage + Multi-sig
├── ML Operational Key → Automated AI workloads
├── Governance Group → Multi-sig for protocol votes
│   ├── CEO/Founder
│   ├── CTO/Tech Lead  
│   └── Head of Operations
└── Treasury Group (Optional) → Separate multi-sig for high-value transfers
    ├── CEO/Founder
    ├── CFO/Finance Lead
    └── Board Member
```

---

# v0: Join New Node Key Management

All steps should be done at the last step of main instruction for [Network Node launch](https://gonka.ai/participant/quickstart).

The key creation procedure will require generating a **cold keypair** and a **warm keypair** and executing several transactions using both of them.
The following steps should be performed on a secure, local machine. A secure machine is one dedicated to managing critical keys and should ideally have:
- Minimal exposure to the public internet (air-gapped is best).
- A minimal installation of software; it is not used for daily web Browse or email.
- An encrypted hard drive.
- Restricted physical and remote access.

At the time of launch, we don't support hardware wallets for managing private keys but they'll be supported soon. For now we recommend storing the private key on a machine with minimal access and protecting it with a password. 
Don't forget to save the mnemonic phrase! After hardware wallets are supported, the private key should be transferred to one. 


Assuming all environment variables from `config.env` are loaded via `source config.env`.

## 1. [Local device]: Create Account Key

For the keyring backend, you should use `os` or `file` for the cold key. This example uses `file` throughout.

```
./inferenced keys add gonka-account-key --keyring-backend file
```

CLI will ask you for passphrase and show data about created key-pair.
```
❯ ./inferenced keys add gonka-account-key --keyring-backend file
Enter keyring passphrase (attempt 1/3):
Re-enter keyring passphrase:

- address: gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm
  name: gonka-account-key
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY"}'
  type: local


**Important** write this mnemonic phrase in a safe place.
It is the only way to recover your account if you ever forget your password.

pyramid sweet dumb critic lamp various remove token talent drink announce tiny lab follow blind awful expire wasp flavor very pair tell next cable
```

**CRITICAL**: Write this mnemonic phrase down and store it in a secure, offline location. 
This phrase is the **only** way to recover your Account Key if you lose your password or access to this machine. 
If you lose the mnemonic, you will permanently lose access to the key and all assets it controls.

## 2. [Server]: Add Account Public Key to server env variables

Edit `config.env` to set `ACCOUNT_PUBKEY`:
```
...
export ACCOUNT_PUBKEY=Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY
``` 
Then load them:
```
source config.env
```

## 3. [Server]: Create ML Operational Key

We'll create warm key inside `api` container. The key is stored in the volume mounted to `/root/.inference` of container.

Keyring backend `file` should be used for warm key.

### First, run a temporary `api` container to create keys:
```
docker compose run --rm --no-deps -it api /bin/sh
```

### Create keys using `KEYRING_PASSWORD` as passphrase:
```
printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | inferenced keys add "$KEY_NAME" --keyring-backend file
```

**Example output:**
```
~ # printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | inferenced keys add "$KEY_NAME" --keyring-backend file

- address: gonka1gyz2agg5yx49gy2z4qpsz9826t6s9xev6tkehw
  name: node-702105
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"Ao8VPh5U5XQBcJ6qxAIwBbhF/3UPZEwzZ9H/qbIA6ipj"}'
  type: local


**Important** write this mnemonic phrase in a safe place.
It is the only way to recover your account if you ever forget your password.

again plastic athlete arrow first measure danger drastic wolf coyote work memory already inmate sorry path tackle custom write result west tray rabbit jeans
```


## 4. [Server & Local machine]: Fund Account Key and Register New Participant

> **Note:** Unfunded HTTP registration (`POST /v1/participants` and `inferenced register-new-participant`) and on-chain `MsgSubmitNewUnfundedParticipant` are **suspended** now that `epoch` and `cosmos` transaction fees are enabled. Every host must fund its Cold Account Key (`gonka-account-key`) first and sign `MsgSubmitNewParticipant` directly on-chain.

### 4.1 [Local machine]: Fund the Cold Account Key with GNK

Before registering on-chain or granting permissions to the Warm Key, transfer GNK (`ngonka`) to the Cold Account address (`gonka1...` from Step 1) so the account exists in `x/auth` and can cover:
- On-chain registration (`MsgSubmitNewParticipant` — `cosmos` fee group, `1 ngonka` min gas price)
- Authz & feegrant setup (`grant-ml-ops-permissions` — `cosmos` fee group, plus the `10 GNK` / `10_000_000_000 ngonka` `BasicAllowance` granted to the Warm Key for `epoch` transactions)
- Any required participant collateral (`MsgDepositCollateral`)

Verify the Cold Account balance:
```bash
./inferenced query bank balances <cold-account-address> --node $SEED_API_URL/chain-rpc/
```

### 4.2 [Server]: Start `tmkms`, `node`, and `proxy` containers

Start `tmkms`, `node`, and `proxy` containers to generate the Consensus Key:
```bash
docker compose up tmkms node proxy -d --no-deps
```

### 4.3 [Server]: Get Consensus Public Key

Query port `26657` on the new `node` container to obtain the base64-encoded ED25519 Consensus Public Key:
```bash
curl http://localhost:26657/status | jq -r '.result.validator_info.pub_key.value'
```

**Example output:**
```
❯ curl http://localhost:26657/status | jq -r '.result.validator_info.pub_key.value'
IytsMYMPIMh+AFe3iYBQAj1Dt3UkIdGvbJCyJwGoJfA=
```

### 4.4 [Local machine]: Register participant on-chain via `tx inference submit-new-participant`

Run `submit-new-participant` on your **local machine** (where the Cold Account Key `gonka-account-key` is stored) using the node's public URL and the Consensus Public Key from Step 4.3. Signing this first transaction with `gonka-account-key` also publishes the Cold Account's `secp256k1` public key in `x/auth`:

```bash
./inferenced tx inference submit-new-participant <new-node-url> \
    --validator-key <consensus-key> \
    --from <account-key-name-in-registry> \
    --keyring-backend file \
    --chain-id gonka-mainnet \
    --gas auto \
    --gas-adjustment 1.5 \
    --gas-prices 1ngonka \
    --node $SEED_API_URL/chain-rpc/ \
    --yes
```

**Example:**
```bash
./inferenced tx inference submit-new-participant \
    http://36.189.234.237:19252 \
    --validator-key "IytsMYMPIMh+AFe3iYBQAj1Dt3UkIdGvbJCyJwGoJfA=" \
    --from gonka-account-key \
    --keyring-backend file \
    --chain-id gonka-mainnet \
    --gas auto \
    --gas-adjustment 1.5 \
    --gas-prices 1ngonka \
    --node http://36.189.234.237:19250/chain-rpc/ \
    --yes
```

Verify registration at `$SEED_API_URL/v1/participants/<cold-account-address>`.

### 5. [Local machine]: Grant Permissions to ML Operational Key

Finally, grant `authz` permissions and the `feegrant` allowance from the Cold Account Key to the ML Operational Key (`warm key`) so the node can submit operational transactions. Because `MsgGrant` and `MsgGrantAllowance` are in the `cosmos` fee group, include `--gas-prices`:

```bash
./inferenced tx inference grant-ml-ops-permissions \
    <account-key-name-in-registry> \
    <ml-operational-key-address> \
    --from <account-key-name-in-registry> \
    --keyring-backend file \
    --chain-id gonka-mainnet \
    --gas 2000000 \
    --gas-prices 1ngonka \
    --node $SEED_API_URL/chain-rpc/ \
    --yes
```

**Example:**
```bash
./inferenced tx inference grant-ml-ops-permissions \
    gonka-account-key \
    gonka1gyz2agg5yx49gy2z4qpsz9826t6s9xev6tkehw \
    --from gonka-account-key \
    --keyring-backend file \
    --chain-id gonka-mainnet \
    --gas 2000000 \
    --gas-prices 1ngonka \
    --node http://36.189.234.237:19250/chain-rpc/ \
    --yes
```

### 6. [Server]: Launch full Node

Then we can launch all containers:
```
docker compose -f docker-compose.mlnode.yml -f docker-compose.yml up -d
```