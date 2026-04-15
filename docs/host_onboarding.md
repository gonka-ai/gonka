# Host Onboarding (with Transaction Fees)

> **Audience:** GPU operators joining Gonka after the v0.2.12 chain upgrade
> activates consensus-level transaction fees, **and** existing hosts upgrading
> from v0.2.11 → v0.2.12.
>
> **TL;DR for existing hosts:** Nothing to do. The v0.2.12 upgrade handler
> automatically creates a feegrant allowance from your cold key to your warm
> key for every existing ML ops authz grant on chain (default: 100 GNK
> spend limit, 1-year expiration). The DAPI auto-discovers the on-chain
> minimum gas price at startup. As long as your cold account has enough
> balance to cover its expected fees, the upgrade is fully transparent.
>
> **For new hosts:** follow the standard quickstart. The existing
> `grant-ml-ops-permissions` step sets up both the authz grants and the
> feegrant allowance automatically, and the DAPI auto-configures its gas
> price from the chain.

This document walks through the **complete onboarding process for a new host**
and the **upgrade procedure for existing hosts** in a network where consensus
fees are enabled.

For the canonical hardware/networking setup, follow
[gonka.ai/host/quickstart](https://gonka.ai/host/quickstart/). This file
focuses specifically on the changes introduced by the transaction-fees feature
and the steps that account for them.

---

## 1. What changed in v0.2.12

### 1.1 Consensus-level transaction fees

The chain now enforces a minimum gas price at **consensus level** (both
`CheckTx` and `DeliverTx`). The default is **10 ngonka per gas unit**,
governance-adjustable via `MsgUpdateParams` without a chain upgrade.

| Parameter | Default | Meaning |
|---|---|---|
| `min_gas_price_ngonka` | `10` | Minimum gas price per gas unit |
| `base_validation_gas` | `500_000` | Extra gas charged on first PoC commit per epoch |
| `gas_per_poc_count` | `100` | Extra gas charged per claimed PoC count (delta-based) |

### 1.2 Fee-exempt protocol-duty messages

The following message types are **always free**, regardless of the on-chain
gas price. They are protocol obligations already rate-limited by other
mechanisms (timing windows, duplicate checks, epoch-scoping, allowlists).

- `MsgSubmitPocBatch`
- `MsgSubmitPocValidationsV2`
- `MsgMLNodeWeightDistribution`
- `MsgValidation`
- `MsgStartInference`, `MsgFinishInference`
- `MsgInvalidateInference`, `MsgRevalidateInference`
- `MsgSubmitDealerPart`, `MsgSubmitVerificationVector`,
  `MsgSubmitGroupKeyValidationSignature`, `MsgSubmitPartialSignature`

### 1.3 Two-component fee on `MsgPoCV2StoreCommit`

To make sybil attacks economically prohibitive, `MsgPoCV2StoreCommit` charges
two extra gas amounts via the gas meter:

- **Once per participant per epoch**: `base_validation_gas` (covers the GPU
  validation cost the network must perform).
- **Per claimed count delta**: `(new_count − previous_count) × gas_per_poc_count`.
  Because deltas sum to the final count, a participant submitting many partial
  updates pays the same total as one that submits one final commit.

This is the primary economic deterrent to creating many fake participants.

### 1.4 The DAPI now pays fees automatically — via feegrant

The DAPI signs every transaction with the **warm key** (ML operational key),
which historically held no balance. After the upgrade, when the warm key
differs from the cold (account) key, the DAPI sets the **cold account as the
fee granter** on every transaction. The chain then deducts fees from the cold
account's balance via the `x/feegrant` allowance you set up at onboarding.

You **never need to fund the warm key**.

You **do** need to ensure the cold account has enough balance to cover its
expected fees over the lifetime of the feegrant allowance (default: 10 GNK).

### 1.5 The DAPI auto-discovers the on-chain gas price

The DAPI is **self-configuring**: at startup it queries the chain for the
current `FeeParams.MinGasPriceNgonka` and uses that value automatically.

Behavior:

| Scenario | DAPI gas price |
|---|---|
| `DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA` set explicitly in `config.env` | Uses the config value (override) |
| Chain has `FeeParams` set (post-upgrade) | Auto-uses on-chain `MinGasPriceNgonka` |
| Chain has `FeeParams` nil (pre-upgrade) | Uses `0` (no fees attached) |

This means **no `config.env` change is required** when upgrading from v0.2.11
to v0.2.12. Cosmovisor restarts the DAPI with the new binary, and the new
binary picks up the on-chain gas price on its own.

The override is only needed if a host wants to pay more than the network
minimum (e.g., to ensure faster inclusion under load). Setting it to a value
**lower** than the chain's minimum is allowed but the chain will reject those
transactions, so don't do that.

---

## 2. Cleanups & onboarding improvements

The onboarding flow already contained an `inferenced tx inference
grant-ml-ops-permissions` step. In v0.2.12 this command does **two** things in
a single transaction:

1. Issues `MsgGrant` messages from cold → warm for every ML ops message type
   (inference, validation, PoC, BLS DKG, etc.) — same as before.
2. Issues `MsgGrantAllowance` from cold → warm with a `BasicAllowance` of
   **10 GNK** spend limit and 1-year expiration.

So **the onboarding command stays the same**, but it now sets up everything
the warm key needs to operate post-upgrade. New hosts following the existing
quickstart get this for free.

When the allowance expires or is depleted, simply re-run the same command. It
re-grants both the authz permissions and the fee allowance.

---

## 3. New host setup (post-v0.2.12)

This is the same flow as
[gonka.ai/host/quickstart](https://gonka.ai/host/quickstart/) with the
fee-related notes added inline.

### 3.1 Local: install CLI and create cold key

```bash
chmod +x inferenced
./inferenced --help

# create the cold (account) key — store the mnemonic OFFLINE
./inferenced keys add gonka-account-key --keyring-backend file
```

> **Important:** the cold key must hold enough balance to fund (a) the
> registration transaction, (b) the `grant-ml-ops-permissions` transaction
> itself, and (c) the ongoing feegrant allowance budget that pays for your
> DAPI's automated transactions. **Recommendation: at least 20 GNK.**
>
> - 10 GNK covers the default feegrant spend limit (good for many months of
>   routine operation).
> - The remaining ~10 GNK comfortably covers registration, grants, occasional
>   collateral deposit/withdraw, and re-grants when the allowance is depleted.

Fund this address from any external wallet (bank send) or via the Gonka
faucet for testnet.

### 3.2 Local: publish the cold key's public key on chain

```bash
./inferenced publish-pubkey \
    --from gonka-account-key \
    --gas-prices 10ngonka \
    --node http://node2.gonka.ai:8000/chain-rpc/
```

This is a 1-ngonka self-transfer that registers your account's pubkey on
chain. **Note the explicit `--gas-prices 10ngonka`** — required after the
v0.2.12 upgrade enables fee enforcement.

### 3.3 Server: clone, configure, and start the chain node

```bash
git clone https://github.com/gonka-ai/gonka.git -b main
cd gonka/deploy/join
cp config.env.template config.env
# fill in config.env via the questionnaire on
# https://gonka.ai/host/quickstart/
source config.env
```

The template already exports `DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA=10`, so
the DAPI will pay the correct fee for any non-exempt automated transactions.
You can override this if a future governance proposal raises the on-chain
minimum.

Start TMKMS + chain node first (before the API):

```bash
docker compose up tmkms node -d --no-deps
docker compose logs tmkms node -f
```

### 3.4 Server: create the warm key inside the API container

```bash
docker compose run --rm --no-deps -it api /bin/sh
# inside the container:
printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" \
    | inferenced keys add "$KEY_NAME" --keyring-backend file
```

Note the warm key address — you will need it for the next step.

### 3.5 Server: register the host with the network

Still inside the API container:

```bash
inferenced register-new-participant \
    $DAPI_API__PUBLIC_URL \
    $ACCOUNT_PUBKEY \
    --node-address $DAPI_CHAIN_NODE__SEED_API_URL
```

This calls the seed node's HTTP `POST /v1/participants` endpoint, which
submits a `MsgSubmitNewUnfundedParticipant` on your behalf. **The seed node
operator pays the registration tx fee** out of their feegrant allowance,
which is why this part doesn't require you to set fees.

### 3.6 Local: grant ML ops permissions and fee allowance

This is the **single command that sets up everything the warm key needs to
operate post-upgrade**:

```bash
./inferenced tx inference grant-ml-ops-permissions \
    gonka-account-key \
    <warm-key-address-from-3.4> \
    --from gonka-account-key \
    --gas-prices 10ngonka \
    --node http://node2.gonka.ai:8000/chain-rpc/
```

What this does in one transaction:

1. **Authz grants** (~20 message types) — the warm key can submit start /
   finish inference, claim rewards, validations, PoC commits, BLS DKG
   messages, etc., on behalf of the cold account.
2. **Feegrant allowance** — the cold account allows the warm key to charge
   up to **10 GNK** of fees against the cold account's balance, for **1 year**.
   The DAPI sets `fee_granter = <cold-address>` on every transaction it sends.

### 3.7 Server: launch the full node

```bash
docker compose -f docker-compose.yml -f docker-compose.mlnode.yml up -d
```

The DAPI will now run normally. Inference and PoC duty messages are
fee-exempt. Reward claims, hardware diff updates, and seed submissions pay
fees from the cold account via the feegrant allowance.

### 3.8 Server: deposit collateral (after epoch 180)

Network operates in a 180-epoch grace period during which collateral is not
required. After the grace period, the host needs to deposit collateral to get
its full network weight (the first 20% is "base weight" granted unconditionally;
the remaining 80% requires backing collateral). See
[gonka.ai/host/collateral](https://gonka.ai/host/collateral/).

```bash
./inferenced tx collateral deposit-collateral 1000000000ngonka \
    --from gonka-account-key \
    --keyring-backend file \
    --gas-prices 10ngonka \
    --node http://node2.gonka.ai:8000/chain-rpc/ \
    --chain-id gonka-mainnet
```

---

## 4. Existing host upgrade procedure (v0.2.11 → v0.2.12)

The upgrade is **fully automatic**. Cosmovisor swaps the binary at the
upgrade block, and the v0.2.12 upgrade handler does three things:

1. Sets `FeeParams` to the chain default (10 ngonka per gas).
2. Iterates every existing cold→warm authz grant and **automatically creates
   a `BasicAllowance` from cold→warm** (default: 100 GNK spend limit,
   1-year expiration). This means the warm key can immediately start paying
   fees from the cold account's balance via `x/feegrant` after the upgrade.
3. The new DAPI binary, on its first start under cosmovisor, queries the
   chain and self-configures its gas price from the on-chain `FeeParams`.

### 4.1 Before the upgrade activates

**The only thing you need to do** is make sure your cold account has enough
balance to cover the expected fee burden over the lifetime of the auto-granted
allowance. Recommended: **at least 100 GNK** (matches the default allowance
spend limit). Hosts who claim significant reward volume may want more.

```bash
./inferenced query bank balances <cold-address> --node ...
```

If short, top up from any external wallet.

### 4.2 Verify after the upgrade

Watch the DAPI logs for a successful claim cycle (next epoch end):

```bash
docker compose logs api -f | grep -E "ClaimRewards|reward"
```

You should see `Using on-chain FeeParams.MinGasPriceNgonka = 10` in the
DAPI startup logs and successful reward claims at each epoch boundary.

### 4.3 If something goes wrong: revoke and re-grant manually

In the unlikely event the auto-migration missed your account (e.g., your
authz grants were structured differently from the standard
`grant-ml-ops-permissions` output), you can manually re-create everything.

If a feegrant allowance already exists from your cold to your warm key but
you want to refresh it, first revoke the existing one:

```bash
./inferenced tx feegrant revoke <warm-address> \
    --from gonka-account-key \
    --gas-prices 10ngonka \
    --node http://node2.gonka.ai:8000/chain-rpc/
```

Then re-run the standard ML ops grant which will recreate both the authz
grants and the feegrant allowance:

```bash
./inferenced tx inference grant-ml-ops-permissions \
    gonka-account-key \
    <warm-address> \
    --from gonka-account-key \
    --gas-prices 10ngonka \
    --node http://node2.gonka.ai:8000/chain-rpc/
```

You can verify the allowance with:

```bash
./inferenced query feegrant grant <cold-address> <warm-address> \
    --node http://node2.gonka.ai:8000/chain-rpc/
```

---

## 5. CLI fee defaults & overrides

All CLI transaction commands accept the standard Cosmos SDK fee flags. After
the upgrade, you must pass `--gas-prices 10ngonka` (or higher) on every
manual `inferenced tx ...` command.

| Command | Fee flag |
|---|---|
| `publish-pubkey` | `--gas-prices 10ngonka` |
| `tx inference grant-ml-ops-permissions` | `--gas-prices 10ngonka` |
| `tx inference submit-new-participant` | `--gas-prices 10ngonka` |
| `tx collateral deposit-collateral` | `--gas-prices 10ngonka` |
| `tx collateral withdraw-collateral` | `--gas-prices 10ngonka` |
| `tx bank send` | `--gas-prices 10ngonka` |
| `tx staking delegate / undelegate / redelegate` | `--gas-prices 10ngonka` |
| `tx gov submit-proposal / vote / deposit` | `--gas-prices 10ngonka` |

You can pass a higher value if a future governance proposal raises
`FeeParams.min_gas_price_ngonka` above 10.

---

## 6. DAPI fee defaults & overrides

The DAPI **auto-discovers** the correct gas price from the chain at startup.
On a pre-upgrade chain (`FeeParams` nil) it sends zero-fee transactions.
On a post-upgrade chain it reads `FeeParams.MinGasPriceNgonka` and pays at
least that much per gas unit.

No manual config action is required. If you want to pay more than the
minimum (e.g., for faster inclusion under load), you can set
`DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA` in `config.env` to override the
auto-discovered value. Any non-zero value in config takes precedence over
the chain query.

```bash
# in config.env (optional — only if you want to pay more than the minimum)
export DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA=15
```

```bash
source config.env
docker compose restart api
```

---

## 7. Common errors and fixes

### "insufficient fee: got , required at least Xngonka"

Your CLI command did not include `--gas-prices`, or your DAPI config has
`min_gas_price_ngonka = 0`.

- For CLI: add `--gas-prices 10ngonka` (or current network minimum) to the
  command.
- For DAPI: set `DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA=10` in `config.env`
  and restart.

### "fee-grant not found"

Your cold→warm feegrant allowance is missing or expired. Re-run
`inferenced tx inference grant-ml-ops-permissions ...` (see §4.3).

### "spendable balance 0ngonka is smaller than Xngonka: insufficient funds"

Your cold account is empty or has been drained. Top it up from an external
wallet.

### "fee allowance expired" (after a long period of operation)

The feegrant allowance has a 1-year expiration by default. Re-run
`grant-ml-ops-permissions` to refresh both the authz and the allowance.

The DAPI logs a clear error message in all of the above cases pointing you to
this document.

---

## 8. Why this works (architecture summary)

```
   Cold key (gonka-account-key)
    │
    │  publish-pubkey ──────────────────────────────────► chain (account creation)
    │
    │  register-new-participant (via seed node)  ───────► chain (Participant record)
    │
    │  grant-ml-ops-permissions ────────────────────────► chain (in one tx):
    │      ├─ MsgGrant × ~20  (authz)                          • cold → warm: can sign these msg types
    │      └─ MsgGrantAllowance (feegrant)                     • cold → warm: spend up to 10 GNK in fees
    │
    │
    └── pays fees for: bank send, collateral, governance, manual tx, etc.


   Warm key (ML operational key, on-server)
    │
    │  signs every DAPI transaction with MsgExec wrapping the inner messages
    │  sets fee_granter = <cold-address> on every transaction
    │
    │  fee-exempt msgs (PoC, validations, inference) ──► no fee charged
    │  fee-required msgs (reward claims, hw diffs)   ──► fee charged to cold via feegrant
    │
    └── never holds balance
```

The warm key remains an unfunded "operational" key. The cold key remains a
high-stakes manual-action key. The feegrant decouples *who signs* from *who
pays* — exactly as Cosmos SDK's `x/feegrant` was designed to enable.

---

## 9. Sybil resistance summary

Transaction fees are not the only sybil defense, but they're a critical layer.
The full picture, after v0.2.12:

| Attack vector | Defense |
|---|---|
| Spam any tx for free | Consensus-level minimum fee (`MinGasPriceNgonka`) |
| Bypass fees by `MsgExec` wrapping | `NetworkDutyFeeBypassDecorator` recursively unpacks `MsgExec` and fails closed if any inner message is non-exempt |
| Spam fee-exempt duty messages | Each duty msg is rate-limited by epoch windows, duplicate checks, deadline enforcement, or validator slot ownership |
| Create many cheap fake participants | `MsgSubmitNewParticipant` requires a fee, paid by the registering account |
| Claim large weight from a fake participant | `MsgPoCV2StoreCommit` charges count-linear gas: more weight ⇒ proportionally larger fee. Combined with the per-epoch base validation cost, sustained sybil attacks become economically prohibitive. |
| Bypass the count-linear fee with many small commits | The handler enforces strictly increasing counts; gas is charged on the **delta**, so total cost = `final_count × gas_per_poc_count` regardless of how many partial commits the attacker submits |

For a deeper analysis, see
[proposals/transaction-fees/README.md](proposals/transaction-fees/README.md).

---

## 10. Quick reference card

| Task | Command |
|---|---|
| Top up cold account | external wallet → `gonka1...` |
| Publish pubkey | `inferenced publish-pubkey --from gonka-account-key --gas-prices 10ngonka --node ...` |
| Register host | `inferenced register-new-participant ...` (via seed node) |
| Grant ML ops + feegrant | `inferenced tx inference grant-ml-ops-permissions <cold> <warm> --from <cold> --gas-prices 10ngonka --node ...` |
| Re-grant after expiration | same command as above |
| Deposit collateral | `inferenced tx collateral deposit-collateral Nngonka --from <cold> --gas-prices 10ngonka --node ...` |
| Check cold balance | `inferenced query bank balances <cold-address> --node ...` |
| Check feegrant allowance | `inferenced query feegrant grant <cold-address> <warm-address> --node ...` |
| Restart DAPI | `docker compose restart api` |
