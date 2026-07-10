# Consumer Setup Guide

Step-by-step guide for sending inference requests to the Gonka network.

> **The most up-to-date version of this guide lives at**
> **<https://gonka.ai/docs/developer/quickstart/>.** If anything here differs from
> the website, the website is authoritative.

## How inference access works today

Gonka inference is organized around **devshards** — short-lived sessions that hold a
small on-chain deposit (an escrow) and settle per-request billing off-chain. The role
of opening a devshard, signing requests, rotating the session, and submitting
settlement to the chain is performed by a piece of software called a **gateway**.

There are two ways to reach the network:

| Path | Who it is for | What you need |
|---|---|---|
| **1. Use a community broker** (recommended) | Most developers | An API key from a broker |
| **2. Run your own gateway** (advanced) | High-throughput / self-custody users | A Gonka account whose address is on the on-chain allow-list |

> **Important — the broker-less CLI path is not self-serve today.** A funded,
> on-chain-registered account that signs its own requests still receives
> `401 {"error":{"message":"model \"...\" requires an API key"}}` for every model,
> because access is gated node-side per model. Sending inference directly with only
> the `inferenced` CLI and your own private key does **not** work end-to-end. To send
> inference you must either use a community broker (option 1) or run your own
> allow-listed gateway (option 2).

---

## 1. Use a community broker (recommended)

A broker is an independent operator who runs a Gonka gateway and resells inference to
developers. From your application's point of view, a broker endpoint behaves like any
OpenAI-compatible API: you set a `base_url`, pass an `Authorization: Bearer <API_KEY>`
header, and call `/v1/chat/completions` as usual.

> Brokers are independent third parties. Pricing, payment methods, rate limits,
> supported models, SLAs, and data handling are determined by each broker. Read the
> broker's own documentation and terms before going live.

### 1.1 Pick a broker and get an API key

Pick a broker from the directory at
[gonka.ai/docs/developer/quickstart](https://gonka.ai/docs/developer/quickstart/#11-pick-a-broker),
then follow the onboarding on the broker's site. Typically you will:

1. Sign up on the broker's site (email, account, billing setup).
2. Generate an API key in the broker's dashboard.
3. Note the broker's `base_url` (for example `https://api.<broker-domain>/v1`) and the
   list of supported models.

### 1.2 Send an inference request

Set the environment variables you got from your broker:

```bash
export GONKA_BROKER_URL=<broker-base-url>     # e.g. https://api.example-broker.com/v1
export GONKA_BROKER_API_KEY=<your-api-key>
export GONKA_MODEL=MiniMaxAI/MiniMax-M2.7   # or any model your broker supports
```

The broker endpoint is OpenAI-compatible, so you can use the official OpenAI SDK
directly — **no Gonka-specific client is required**.

```bash
pip install openai
```

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url=os.environ["GONKA_BROKER_URL"],
    api_key=os.environ["GONKA_BROKER_API_KEY"],
)

response = client.chat.completions.create(
    model=os.environ["GONKA_MODEL"],
    messages=[{"role": "user", "content": "Hello!"}],
)

print(response.choices[0].message.content)
```

Model IDs are case-sensitive — copy them exactly, e.g.
`MiniMaxAI/MiniMax-M2.7`.

For the full set of language examples (TypeScript, Go), tool calling, and no-code app
integrations (Open WebUI, Cursor, n8n, etc.), see the
[Developer Quickstart](https://gonka.ai/docs/developer/quickstart/).

---

## 2. Run your own gateway (advanced)

If your application has high throughput, or you want to pay GNK directly on-chain
instead of going through a broker, you can run a Gonka gateway yourself. The gateway is
a small program (shipped as a Docker container) that you run on your own machine or
server — never on a Gonka host. It exposes the same OpenAI-compatible API as a broker,
but you own the keys and you pay GNK directly on-chain for the devshards it creates.

> **Self-hosted gateways require an allow-listed address.** Today, only Gonka accounts
> on the on-chain `devshard_escrow_params.allowed_creator_addresses` list can open
> devshards. If your address is not on that list, your gateway cannot create sessions
> and you cannot send inference. The allow-list is changed only by on-chain governance
> vote.

Full deployment instructions are in
[Run your own gateway](https://gonka.ai/docs/developer/gateway-developer-quickstart/).

To request consideration for on-chain allow-listing, open a GitHub issue including your
operator name and contact, the `gonka1...` address you intend to use, and the models
you plan to serve. Inclusion is an on-chain governance decision and expressing interest
does not guarantee inclusion or a timeline.

---

## Managing a Gonka account

If you use a community broker (option 1), you do **not** need your own Gonka account —
the broker handles GNK and on-chain settlement for you. You only need an account if you
plan to run your own gateway, pay GNK directly on-chain, or otherwise participate in the
network. The `inferenced` CLI manages keys and on-chain operations.

### Install the `inferenced` CLI

Download the latest `inferenced` binary for your system from the
[official repository](https://github.com/gonka-ai/gonka).

```bash
chmod +x inferenced
sudo mv inferenced /usr/local/bin/
inferenced version
```

**macOS:** if you see a security warning, go to **System Settings → Privacy & Security**
and click "Allow Anyway".

### Create an account

```bash
export ACCOUNT_NAME="myaccount"
export NODE_URL="http://node2.gonka.ai:8000"

inferenced keys add "$ACCOUNT_NAME"
```

The output contains your **address**, **public key**, and **mnemonic phrase**.

> **Important:** Back up the mnemonic phrase and private key securely — they are the
> only way to recover the account.

```bash
export GONKA_ADDRESS="<address from the output>"
```

### Fund the account and publish your public key

For a full guide on wallets, balances, and transfers see the
[Wallet & Transfer Guide](https://gonka.ai/docs/wallet/wallet-and-transfer-guide/).

Check your balance:

```bash
inferenced query bank balances "$GONKA_ADDRESS" --node "$NODE_URL/chain-rpc"
```

Fund the account by sending `ngonka` from another wallet:

```bash
inferenced tx bank send <sender-key-name> "$GONKA_ADDRESS" 1000000ngonka \
  --chain-id gonka-mainnet \
  --node "$NODE_URL/chain-rpc"
```

Once funded, publish your public key on-chain:

```bash
inferenced publish-pubkey \
  --from "$ACCOUNT_NAME" \
  --node "$NODE_URL/chain-rpc" \
  --yes
```

> If you get `rpc error: code = NotFound ... account ... not found`, your account has
> not received tokens yet — fund it first.

Verify the account:

```bash
curl -s "$NODE_URL/v2/accounts/$GONKA_ADDRESS" | jq .
```

The response should include `pubkey`, `balance`, and `denom`.

### Key Management Reference

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
