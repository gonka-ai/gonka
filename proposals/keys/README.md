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


## 4. [Server]: Register new participant

To register new participant, we need to send new participants details to the seed node:
1. Public Key for Account Key - we've created at local device: `"Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY"`
2. Public Key for Consensus Key - corresponding private key is generated inside `tmkms` containers and shouldn't leave it. 

### 4.1 [Server]: Start all containers excluding `api`:

We can start `tmkms`, `node` and `proxy` containers to generate Consensus Key and endpoint to receive it:
```
docker compose up tmkms node proxy -d --no-deps
```

### 4.2 [From server]: Get Consensus Public Key

Use 26657 port for your new `node` container to get Consensus Public Key
```
curl http://localhost:26657/status | jq -r '.result.validator_info.pub_key.value'
```

**Example output:**
```
❯ curl http://localhost:26657/status | jq -r '.result.validator_info.pub_key.value'
IytsMYMPIMh+AFe3iYBQAj1Dt3UkIdGvbJCyJwGoJfA=
```


### 4.3 [Any machine]: Register participant via `register-new-participant`

This command doesn't require signing from either the cold or warm private key and can be executed on any machine.

#### Auto-Fetch Mode (Recommended for API containers)
When running from within API containers, the consensus key can be automatically fetched from the chain node:
```
./inferenced register-new-participant \
    <new-node-url> \
    <account-public-key> \
    --node-address $SEED_API_URL
```

#### Manual Mode (For external usage)
For external usage or when auto-fetch is not available, provide the consensus key explicitly:
```
./inferenced register-new-participant \
    <new-node-url> \
    <account-public-key> \
    --consensus-key <consensus-key> \
    --node-address $SEED_API_URL
```

**Note**: Auto-fetch mode requires the `DAPI_CHAIN_NODE__URL` environment variable to be set (automatically configured in API containers). If auto-fetch fails, the command will provide clear guidance on using manual mode.

#### Example: Auto-Fetch Mode (from API container)

```
❯ ./inferenced register-new-participant \
    http://36.189.234.237:19252 \
    "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY" \
    --node-address http://36.189.234.237:19250

No consensus key provided, attempting to auto-fetch from chain node...
Successfully auto-fetched and validated consensus key from chain node
Registering new participant:
  Node URL: http://36.189.234.237:19252
  Account Address: gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm
  Account Public Key: Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY
  Validator Consensus Key: IytsMYMPIMh+AFe3iYBQAj1Dt3UkIdGvbJCyJwGoJfA= (auto-fetched)
  Seed Node Address: http://36.189.234.237:19250
Sending registration request to http://36.189.234.237:19250/v1/participants
Response status code: 200
Participant registration successful.
Waiting for participant to be available (timeout: 30 seconds)...
..
Found participant with pubkey: Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY (balance: 0)
Participant is now available at http://36.189.234.237:19250/v1/participants/gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm
```

#### Example: Manual Mode (external usage)

```
❯ ./inferenced register-new-participant \
    http://36.189.234.237:19252 \
    "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY" \
    --consensus-key "IytsMYMPIMh+AFe3iYBQAj1Dt3UkIdGvbJCyJwGoJfA=" \
    --node-address http://36.189.234.237:19250

Using provided consensus key (validated)
Registering new participant:
  Node URL: http://36.189.234.237:19252
  Account Address: gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm
  Account Public Key: Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY
  Validator Consensus Key: IytsMYMPIMh+AFe3iYBQAj1Dt3UkIdGvbJCyJwGoJfA= (provided)
  Seed Node Address: http://36.189.234.237:19250
Sending registration request to http://36.189.234.237:19250/v1/participants
Response status code: 200
Participant registration successful.
Waiting for participant to be available (timeout: 30 seconds)...
..
Found participant with pubkey: Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY (balance: 0)
Participant is now available at http://36.189.234.237:19250/v1/participants/gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm
```

#### Troubleshooting `register-new-participant`

**Auto-fetch fails with environment variable error:**
- Ensure you're running from within an API container where `DAPI_CHAIN_NODE__URL` is automatically set
- For external usage, use manual mode with `--consensus-key` flag

**Auto-fetch fails with connection error:**
- Verify the chain node is running and accessible
- Check network connectivity between API container and chain node
- Use manual mode as fallback

**Invalid consensus key error:**
- Ensure the consensus key is exactly 32 bytes when base64-decoded (ED25519 requirement)
- Verify the key was obtained from the correct chain node status endpoint
- For manual debugging, use: `curl http://localhost:26657/status | jq -r '.result.validator_info.pub_key.value'`

**Missing validator information:**
- The node may not be configured as a validator
- Ensure TMKMS and node containers are running properly
- Use manual mode with explicit consensus key from TMKMS

### 5. [Local machine]: Grant Permissions to ML Operational Key

Finally, we need to grant permissions from Account Key to ML Operational Key to create transactions required for proper node operation.  
This can be done using `grant-ml-ops-permissions` and signed by Account Key:

```
./inferenced tx inference grant-ml-ops-permissions \
    <account-key-name-in-registry> \
    <ml-operational-key-address> \
    --from <account-key-name-in-registry> \
    --gas 2000000 \
    --node $SEED_API_URL/chain-rpc/
```

**Example output:**
```
./inferenced tx inference grant-ml-ops-permissions \
    gonka-account-key \
    gonka1gyz2agg5yx49gy2z4qpsz9826t6s9xev6tkehw \
    --from gonka-account-key \
    --keyring-backend file \
    --gas 2000000 \
    --node http://36.189.234.237:19250/chain-rpc/

```

### 6. [Server]: Launch full Node

Then we can launch all containers:
```
docker compose -f docker-compose.mlnode.yml -f docker-compose.yml up -d
```