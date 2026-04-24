# Terraform Deployment For `0.2.10`

This Terraform setup is intentionally practical:

- It **does not create Nebius VMs** from scratch.
- It **does** manage deployment onto the **existing 3 hosts** you already use.
- It uploads the scripts in this folder, writes **correct per-node env files**, runs **genesis first**, waits for the public genesis API, then runs **join-1** and **join-2**.

This is the part that avoids the manual mistakes we already hit:

- missing exported vars like `API_PORT`
- wrong `CHAIN_ID`
- wrong seed RPC/API values on join nodes
- running steps in the wrong order

It is designed for **initial deployment on `0.2.10`**.  
After that, do the **`0.2.11` on-chain upgrade manually**.

## What Terraform Manages

- Upload `launch.py` and `genesis-overrides.json` to `/srv/dai`
- Render a per-node env file:
  - `tf-genesis.env`
  - `tf-join_1.env`
  - `tf-join_2.env`
- Render a per-node run script:
  - `tf-genesis-run.sh`
  - `tf-join_1-run.sh`
  - `tf-join_2-run.sh`
- Run:
  - genesis node
  - wait for `http://<genesis>:19240/v1/identity`
  - join-1
  - join-2

## What Terraform Does Not Manage

- Nebius VM creation/destruction
- Governance proposal submission
- Voting
- Manual upgrade from `0.2.10` to `0.2.11`

That split is deliberate. Terraform is good at infra and deterministic deployment. It is not good at stateful on-chain governance logic.

## Files

- `main.tf` - deployment order and remote execution
- `variables.tf` - ports, domain, SSH, chain ID
- `outputs.tf` - SSH and curl helpers
- `terraform.tfvars.example` - copy this to `terraform.tfvars`

## First-Time Setup

From `test-net-cloud/nebius`:

```bash
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars` if any of these changed:

- SSH key path
- chain id
- host ports
- internal IPs
- branch

## Apply

```bash
terraform init
terraform plan
terraform apply
```

## What Happens During Apply

1. Terraform connects to all 3 servers over SSH.
2. It uploads the current `launch.py` and `genesis-overrides.json`.
3. It writes the right env file for each node.
4. It runs genesis.
5. It waits until the genesis public API responds.
6. It runs join-1 and join-2.

## After Apply

Useful checks:

```bash
curl -sS http://xj7-5.s.filfox.io:19240/v1/identity | jq .
curl -sS http://xj7-5.s.filfox.io:19250/v1/identity | jq .
curl -sS http://xj7-5.s.filfox.io:19252/v1/identity | jq .
```

```bash
curl -sS http://xj7-5.s.filfox.io:19240/v1/participants | jq .
curl -sS http://xj7-5.s.filfox.io:19240/v1/governance/models | jq .
```

## Upgrade Strategy

Use Terraform only for the fresh `0.2.10` deploy.

Then upgrade manually:

1. submit upgrade proposal
2. vote
3. wait for height
4. inspect cosmovisor / node logs

That keeps Terraform simple and avoids mixing infra convergence with on-chain state transitions.

## Notes

- This Terraform uses the **existing host fleet** and your known Filfox/Nebius port layout.
- If later you want full Nebius VM creation in Terraform too, add that as a second layer after this deployment flow is stable.
