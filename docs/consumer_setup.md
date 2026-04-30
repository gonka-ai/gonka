# Consumer Setup Guide

Step-by-step guide for creating a developer account and sending inference requests to the Gonka network.

---

## 1. Define Variables

Before starting, set the required environment variables:

```bash
export ACCOUNT_NAME="myaccount"
export NODE_URL="http://node2.gonka.ai:8000"
```

| Variable | Description |
|---|---|
| `ACCOUNT_NAME` | Local keyring name for your account. Exists only on your machine — not recorded on-chain. |
| `NODE_URL` | URL of any Gonka node. Used for account queries, inference requests, and on-chain operations. |

Replace `myaccount` with any name you like and pick a node URL from the list below.

<details>
<summary><b>Genesis nodes</b></summary>

```
http://node1.gonka.ai:8000
http://node2.gonka.ai:8000
http://node3.gonka.ai:8000
http://185.216.21.98:8000
http://36.189.234.197:18026
http://36.189.234.237:17241
http://47.236.26.199:8000
http://47.236.19.22:18000
http://gonka.spv.re:8000
```

</details>

<details>
<summary><b>How to pick a random active node instead</b></summary>

Fetch the current list of active participants and pick any `inference_url`:

```bash
curl "$NODE_URL/v1/epochs/current/participants"
```

Using a random node improves network decentralization and load distribution.

</details>

---

## 2. Install the `inferenced` CLI

Download the latest `inferenced` binary for your system from the [official repository](https://github.com/gonka-ai/gonka).

```bash
chmod +x inferenced
sudo mv inferenced /usr/local/bin/
inferenced version
```

**macOS:** if you see a security warning, go to **System Settings → Privacy & Security** and click "Allow Anyway".

---

## 3. Create an Account

Create a local keypair that will be used to sign inference requests.

```bash
inferenced keys add "$ACCOUNT_NAME"
```

The output contains your **address**, **public key**, and **mnemonic phrase**.

> **Important:** Back up the mnemonic phrase and private key securely — they are the only way to recover the account and sign requests.

Save the address for later steps:

```bash
export GONKA_ADDRESS="<address from the output>"
```

You will also need your private key in Step 5 to configure the client connector. How you store and supply it (environment variable, secrets manager, `.env` file, etc.) is up to you.

---

## 4. Fund the Account and Publish Your Public Key

To send inference requests, your account must have a positive balance and its public key must be published on-chain.
You do **not** need to register as a Participant — that is only required for inference hosting.

### 4.1 Fund the account

Your account needs a positive balance to pay for inference. For a full guide on wallets, balances, and transfers see the [Wallet & Transfer Guide](https://gonka.ai/wallet/wallet-and-transfer-guide/).

You can check your current balance at any time:

```bash
inferenced query bank balances "$GONKA_ADDRESS" --node "$NODE_URL/chain-rpc"
```

To fund the account, send any amount of `ngonka` from another wallet:

```bash
inferenced tx bank send <sender-key-name> "$GONKA_ADDRESS" 1000000ngonka \
  --chain-id gonka-mainnet \
  --node "$NODE_URL/chain-rpc"
```

You can also transfer funds using [Keplr](https://gonka.ai/wallet/wallet-and-transfer-guide/#send-coins) or [Leap](https://gonka.ai/wallet/wallet-and-transfer-guide/#send-coins) wallets.

### 4.2 Publish the public key on-chain

Once the account is funded, publish your public key:

```bash
inferenced publish-pubkey \
  --from "$ACCOUNT_NAME" \
  --node "$NODE_URL/chain-rpc" \
  --yes
```

This performs a minimal self-transfer (`1ngonka`) that registers your public key on the blockchain.

> If you get `rpc error: code = NotFound ... account ... not found`, your account has not received tokens yet — complete step 4.1 first.

### 4.3 Verify the account

```bash
curl -s "$NODE_URL/v2/accounts/$GONKA_ADDRESS" | jq .
```

The response should include `pubkey`, `balance`, and `denom`.

---

## 5. Send an Inference Request

Gonka uses a modified OpenAI SDK that automatically signs every request with your private key. Full documentation and examples for all supported languages are in the [gonka-openai repository](https://github.com/gonka-ai/gonka-openai/).

### Minimal Python example

```bash
pip install gonka-openai
```

```python
from gonka_openai import GonkaOpenAI

client = GonkaOpenAI(
    gonka_private_key="<your-private-key>",  # hex-encoded private key from Step 3
    source_url="<NODE_URL>",                 # same node URL from Step 1
)

response = client.chat.completions.create(
    model="Qwen/Qwen2.5-7B-Instruct",
    messages=[{"role": "user", "content": "Hello!"}],
    max_tokens=128,
)

print(response.choices[0].message.content)
```

> If you get `Insufficient balance`, fund the account with more tokens or lower `max_tokens`.

---

## Key Management Reference

```bash
# List all accounts
inferenced keys list

# Show public key
inferenced keys show "$ACCOUNT_NAME" --pubkey

# Recover an account from mnemonic
inferenced keys add "$ACCOUNT_NAME" --recover

# Delete an account (use with caution)
inferenced keys delete "$ACCOUNT_NAME"

# Export private key (use carefully)
inferenced keys export "$ACCOUNT_NAME" --unarmored-hex --unsafe
```
