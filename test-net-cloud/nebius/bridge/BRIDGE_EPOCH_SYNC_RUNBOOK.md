# Bridge epoch sync — runbook

Automatically push Gonka BLS epoch group keys to the Sepolia `BridgeContract` after each Gonka epoch (~30 minutes on testnet). Gonka chain/API do **not** do this themselves; this watcher calls `bridge-enable-normal-op.js` on a schedule.

## Files in this repo

| File | Purpose |
|------|---------|
| [`bridge-epoch-sync.sh`](bridge-epoch-sync.sh) | Main watcher (loop or `--once`) |
| [`bridge-epoch-sync.env.example`](bridge-epoch-sync.env.example) | Env template (copy to server, **never commit secrets**) |
| [`bridge-epoch-sync.service.example`](bridge-epoch-sync.service.example) | systemd unit template |
| [`bridge-enable-normal-op.js`](bridge-enable-normal-op.js) | Node script that submits `submitGroupKey` on Ethereum |

**Server layout (recommended):**

```text
/srv/dai/
  bridge-epoch-sync.sh          ← copy from repo (or symlink)
  bridge-epoch-sync.env         ← secrets, chmod 600
  gonka/                        ← git checkout
    proposals/ethereum-bridge-contact/
    test-net-cloud/nebius/bridge/
```

---

## Prerequisites

- Gonka node/API up (`http://localhost:8000` reachable)
- Sepolia bridge contract in **`NORMAL_OPERATION`** (one-time bootstrap via `bridge-enable-normal-op.js` without `--target-epoch`)
- Ethereum wallet is **`BridgeContract.owner()`** and has Sepolia ETH (~0.001 ETH per epoch sync; see cost notes below)
- `node` (v18+), `npm`, `jq`, `curl` installed

Install Node deps once:

```bash
cd /srv/dai/gonka/proposals/ethereum-bridge-contact
npm install
```

---

## 1. Configure environment

```bash
cp /srv/dai/gonka/test-net-cloud/nebius/bridge/bridge-epoch-sync.env.example \
   /srv/dai/bridge-epoch-sync.env

chmod 600 /srv/dai/bridge-epoch-sync.env
nano /srv/dai/bridge-epoch-sync.env
```

**Required variables:**

```bash
PRIVATE_KEY=0x...                    # bridge owner key (must match contract owner)
BRIDGE_ADDRESS=0x8395733b8ecc2d1d3a7eb1b8b921d71ee4620b02
GONKA_API=http://localhost:8000
SEPOLIA_RPC_URL=https://ethereum-sepolia-rpc.publicnode.com

# Full paths when script is installed at /srv/dai/bridge-epoch-sync.sh
BRIDGE_DIR=/srv/dai/gonka/proposals/ethereum-bridge-contact
ENABLE_SCRIPT=/srv/dai/gonka/test-net-cloud/nebius/bridge/bridge-enable-normal-op.js

POLL_SECONDS=30
VERBOSE=0
HEARTBEAT_EVERY=1
LOG_FILE=/var/log/bridge-epoch-sync.log
```

The shell script **sources** `/srv/dai/bridge-epoch-sync.env` automatically (or `bridge-epoch-sync.env` next to the script).

---

## 2. Install the script

Copy from repo after each `git pull`:

```bash
sudo cp /srv/dai/gonka/test-net-cloud/nebius/bridge/bridge-epoch-sync.sh \
        /srv/dai/bridge-epoch-sync.sh
sudo chmod 750 /srv/dai/bridge-epoch-sync.sh
sudo chown root:ubuntu /srv/dai/bridge-epoch-sync.sh
```

Optional log file:

```bash
sudo touch /var/log/bridge-epoch-sync.log
sudo chown ubuntu:ubuntu /var/log/bridge-epoch-sync.log
```

---

## 3. Test without systemd

**Single poll:**

```bash
/srv/dai/bridge-epoch-sync.sh --once
```

**Foreground loop** (Ctrl+C to stop):

```bash
/srv/dai/bridge-epoch-sync.sh
```

**More detail:**

```bash
VERBOSE=1 /srv/dai/bridge-epoch-sync.sh --once
```

Expect lines like:

```text
INFO  poll=1 gonka_epoch=1367 bridge_epoch=1367 behind=0 ...
INFO  poll=1 heartbeat caught_up gonka=1367 bridge=1367
INFO  poll=1 done; sleeping 30s
```

If **`behind=N`** with N > 0, the first poll submits N epochs on Ethereum (several minutes). Live progress:

```text
INFO  sync Submitting epoch 1335...
INFO  sync Tx:       0x...
INFO  sync Confirmed in block 11121950
```

---

## 4. Install and start systemd daemon

```bash
sudo cp /srv/dai/gonka/test-net-cloud/nebius/bridge/bridge-epoch-sync.service.example \
        /etc/systemd/system/bridge-epoch-sync.service

# Edit User/Group/ExecStart if your paths differ
sudo nano /etc/systemd/system/bridge-epoch-sync.service

sudo systemctl daemon-reload
sudo systemctl enable bridge-epoch-sync
sudo systemctl start bridge-epoch-sync
```

**Example unit** (`bridge-epoch-sync.service.example`):

```ini
[Unit]
Description=Sync Gonka BLS epochs to Ethereum bridge contract
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
User=ubuntu
Group=ubuntu
WorkingDirectory=/srv/dai
EnvironmentFile=/srv/dai/bridge-epoch-sync.env
ExecStart=/srv/dai/bridge-epoch-sync.sh
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

The service **survives SSH logout** and **restarts on reboot** (`enable`).

After changing the script or env:

```bash
sudo systemctl restart bridge-epoch-sync
```

---

## 5. How to check logs

### systemd journal (primary)

```bash
# Is it running?
sudo systemctl status bridge-epoch-sync

# Live follow
sudo journalctl -u bridge-epoch-sync -f

# Last 50 lines
sudo journalctl -u bridge-epoch-sync -n 50 --no-pager

# Since today
sudo journalctl -u bridge-epoch-sync --since today
```

### File log (optional)

```bash
tail -f /var/log/bridge-epoch-sync.log
```

### Log line meanings

| Prefix | Meaning |
|--------|---------|
| `INFO  poll=N gonka_ready=… bridge_epoch=… behind=…` | Each poll: highest BLS-ready Gonka epoch, Ethereum bridge epoch, lag |
| `INFO  poll=N gonka_ready=… gonka_effective=… gonka_latest=…` | During PoC: ready epoch vs effective vs dashboard latest |
| `INFO  caught up; PoC in progress (gonka_latest=… BLS pending)` | Dashboard advanced but next epoch BLS not ready — no Ethereum tx attempted |
| `INFO  catching up: submitting up to N epoch(s)` | Catch-up burst (can take many minutes) |
| `INFO  sync Submitting epoch …` / `sync Tx:` | Live Ethereum submission |
| `OK    submitted epoch(s) through …` | Epoch(s) submitted successfully |
| `INFO  heartbeat caught_up` | Idle, bridge matches Gonka |
| `WAIT  … BLS validation_signature not ready` | Normal right after Gonka epoch flip; retries next poll |
| `ERR   …` | Misconfiguration, RPC failure, wrong owner key, etc. |
| `INFO  poll=N done; sleeping 30s` | End of poll cycle |

### Verify on-chain

**Gonka epochs** (dashboard shows **latest**; bridge sync submits only **BLS-ready** epochs):

```bash
# Effective epoch (validators currently active)
curl -s http://localhost:8000/chain-api/productscience/inference/inference/get_current_epoch | jq .

# Latest epoch (what the validator dashboard shows during PoC)
curl -s http://localhost:8000/chain-api/productscience/inference/inference/epoch_info \
  | jq '{latest_epoch: .latest_epoch.index, block_height: .block_height}'

# BLS readiness for the next epoch (required before submitGroupKey)
curl -s http://localhost:8000/chain-api/productscience/inference/bls/epoch_data/1368 \
  | jq '{epoch_id: .epoch_data.epoch_id, has_validation_sig: (.epoch_data.validation_signature != null)}'
```

During PoC, `latest_epoch` is often **effective + 1**. The sync daemon waits until `validation_signature` exists for epoch `bridge+1` before calling Ethereum. `behind=0` with `gonka_latest > bridge` is normal while BLS is pending.

**Sepolia bridge** — recent **Submit Group Key** txs on [Etherscan](https://sepolia.etherscan.io/address/0x8395733b8ecc2d1d3a7eb1b8b921d71ee4620b02) from the owner wallet.

When healthy: `behind=0` in logs and Gonka epoch ≈ bridge `latestEpochId`.

---

## 6. Operations cheat sheet

| Action | Command |
|--------|---------|
| Start | `sudo systemctl start bridge-epoch-sync` |
| Stop | `sudo systemctl stop bridge-epoch-sync` |
| Restart | `sudo systemctl restart bridge-epoch-sync` |
| Disable on boot | `sudo systemctl disable bridge-epoch-sync` |
| Is active? | `sudo systemctl is-active bridge-epoch-sync` |

---

## 7. Cost (Sepolia)

Per **`submitGroupKey`** transaction: **~443,000 gas** → **~0.0009–0.001 ETH** at ~2 Gwei.

With Gonka epochs **~every 30 minutes**: **~0.05 ETH/day** steady state. Keep **≥0.1 ETH** on the owner wallet for headroom; catch-up after downtime costs **N × ~0.001 ETH** for N missed epochs.

---

## 8. Troubleshooting

| Symptom | Fix |
|---------|-----|
| `BRIDGE_CONTRACT_DIR not found` | Set `BRIDGE_DIR=/srv/dai/gonka/proposals/ethereum-bridge-contact` in env |
| `bridge-enable-normal-op.js not found` | Set `ENABLE_SCRIPT=.../gonka/test-net-cloud/nebius/bridge/bridge-enable-normal-op.js` |
| `Cannot find module 'ethers'` | `cd .../ethereum-bridge-contact && npm install` |
| Poll 1 then silence for minutes | Normal during catch-up (`behind>0`); watch `sync Submitting epoch` or Etherscan |
| Dashboard epoch ahead of logs | Normal during PoC: dashboard shows `latest_epoch`, logs show `gonka_ready` (BLS-ready). Bridge stays on previous epoch until `validation_signature` exists. |
| `WAIT … validation_signature not ready` | Should be rare now (script skips submit when BLS missing). If seen, BLS ceremony still in progress. |
| `Signer is not the BridgeContract owner` | Wrong `PRIVATE_KEY` |
| Permission denied on script | `chmod 750 /srv/dai/bridge-epoch-sync.sh` |
| Env not readable | `chown root:ubuntu bridge-epoch-sync.env && chmod 640` or run service as user that owns env |

---

## 9. Related docs

- [BRIDGE_TESTNET_GUIDE.md](BRIDGE_TESTNET_GUIDE.md) — full bridge setup, bootstrap, wrap/unwrap
- GitHub issue [#1358](https://github.com/gonka-ai/gonka/issues/1358) — inbound bridge reliability during chain halt
