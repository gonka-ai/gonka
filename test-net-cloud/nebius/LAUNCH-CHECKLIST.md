# Testnet Launch Checklist (Claude)

Run these checks after `deploy.sh` completes. All commands assume `.env` is sourced and `INFERENCED=/srv/dai/inferenced` on the genesis node, `NODE=http://<domain>:<genesis_api_port>/chain-rpc/`.

---

## 1. Binary version consistency

Check that the binary on disk, the running node container, and the build source all match.

```bash
# Version on disk
ssh -p $GENESIS_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN "/srv/dai/inferenced version"

# Version running inside the node container
ssh -p $GENESIS_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN "docker exec node inferenced version"

# Repeat for join-1 and join-2
ssh -p $JOIN1_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN "docker exec node inferenced version"
ssh -p $JOIN2_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN "docker exec node inferenced version"
```

All three should return the same version string. If node container differs from disk, cosmovisor may be running the old binary — check `/srv/dai/.inference/cosmovisor/`.

---

## 2. tmkms — chain ID and key assignment

```bash
# Check chain_id in tmkms config points to testnet, not mainnet
ssh -p $GENESIS_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN \
  "docker exec tmkms cat /etc/tmkms/tmkms.toml | grep chain_id"

# Confirm tmkms is signing for the right consensus key
ssh -p $GENESIS_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN \
  "docker logs tmkms --tail 20 2>&1 | grep -i 'connected\|chain\|key\|error'"
```

Expected: `chain_id = "gonka-testnet-3"` (not `gonka-mainnet`). tmkms logs should show `Connected to validator`.

---

## 3. mlnode version

```bash
ssh -p $GENESIS_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN \
  "docker inspect join-mlnode-308-1 | python3 -c \"import json,sys; cfg=json.load(sys.stdin)[0]; img=cfg['Config']['Image']; print(img)\""

# Repeat for join-1 and join-2
```

Expected version: `3.0.12-past4`. If `3.0.13` or higher appears, flag it — only deploy mlnode versions that have been validated on this testnet.

---

## 4. Devshardd containers

```bash
for port in $GENESIS_SSH_PORT $JOIN1_SSH_PORT $JOIN2_SSH_PORT; do
  echo "=== port $port ==="
  ssh -p $port $SSH_USER@$PUBLIC_DOMAIN \
    "docker ps --filter name=devshard --format '{{.Names}} {{.Status}}'"
done
```

Each node should show devshardd running and healthy. If missing, check `docker-compose.yml` for the devshardd service.

---

## 5. Validator registration — gentx / register-validator not rejected

```bash
# Check the node's validator is in the active set
inferenced query staking validators --node "$NODE" -o json | \
  python3 -c "
import json,sys
vs=json.load(sys.stdin).get('validators',[])
print(f'{len(vs)} validators')
for v in vs:
    print(v.get('status'), v.get('description',{}).get('moniker'), v.get('operator_address','')[:30])
"

# Check for any slashing / tombstone
inferenced query slashing signing-infos --node "$NODE" -o json | \
  python3 -c "
import json,sys
infos=json.load(sys.stdin).get('info',[])
for i in infos:
    if i.get('tombstoned') or int(i.get('missed_blocks_counter','0')) > 0:
        print('WARN', i)
"
```

All validators should be `BOND_STATUS_BONDED` and not tombstoned.

---

## 6. Governance models registered

```bash
inferenced query inference params --node "$NODE" 2>&1 | grep -A3 "models:"
```

Check that all expected models appear under `poc_params.models[]`. Each model needs `model_id`, `weight_scale_factor`, and `seq_len`. If a model is missing, it was not included in the genesis or upgrade handler — submit a governance proposal.

Also check the epoch group data shows the models in the active subgroup:

```bash
inferenced query inference current-epoch-group-data --node "$NODE" 2>&1 | grep "sub_group_models" -A10
```

---

## 7. Nodes becoming participants — PoC commits landing on chain

```bash
# Count registered participants
inferenced query inference count-participants --node "$NODE"

# List them with weight
inferenced query inference list-participant --node "$NODE" -o json | \
  python3 -c "
import json,sys
ps=json.load(sys.stdin).get('participant',[])
print(f'{len(ps)} participants')
for p in ps:
    print(p.get('index','?')[:40], 'weight='+str(p.get('weight')), 'epochs='+str(p.get('epochs_completed',0)))
"

# Wait for PoC stage, then check commits (replace HEIGHT with current poc_start_block_height)
inferenced query inference get-current-epoch --node "$NODE"
inferenced query inference all-poc-v2-store-commits <poc_start_height> --node "$NODE"
```

Expected: all join nodes appear with positive weight after 1–2 epochs of PoC. If weight stays at `-1` after 2 epochs, check item 8 below.

---

## 8. Fee grants and authz — DAPI warm key permissions

This is the most common silent failure: the node looks healthy but nothing lands on chain.

```bash
# For each join node, get the DAPI warm key address
ssh -p $JOIN1_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN \
  "docker exec api sh -c 'echo 12345678 | inferenced keys show join-1 --keyring-backend file --keyring-dir /root/.inference 2>&1' | grep address"

ssh -p $JOIN2_SSH_PORT $SSH_USER@$PUBLIC_DOMAIN \
  "docker exec api sh -c 'echo 12345678 | inferenced keys show join-2 --keyring-backend file --keyring-dir /root/.inference 2>&1' | grep address"

# Then check fee grants TO each warm key
inferenced query feegrant grants-by-grantee <warm_key_address> --node "$NODE"
```

Expected: one `BasicAllowance` per warm key, granter = that node's participant (cold key) address, spend limit = 10 GNK, expiration ~1 year out.

If missing, run on the affected node:

```bash
ssh -p <node_ssh_port> $SSH_USER@$PUBLIC_DOMAIN "
echo 12345678 | /srv/dai/inferenced tx inference grant-ml-ops-permissions \
  gonka-account-key <warm_key_address> \
  --from gonka-account-key \
  --keyring-backend file \
  --keyring-dir /srv/dai/.inference \
  --node http://<seed_api>/chain-rpc/ \
  --chain-id $CHAIN_ID \
  --gas 2000000 \
  -y
"
```

Missing fee grant causes: PoC commits silently fail, hardware diffs rejected, validator 401 on proof requests, weight stays at -1 forever.

Also check authz grants exist (set by the same command):

```bash
inferenced query authz grants <cold_key_addr> <warm_key_addr> --node "$NODE" | grep "msg:" | wc -l
```

Expected: ~20 message types granted. If 0, re-run `grant-ml-ops-permissions`.
