# Ops Quickstart (`.env` + scripts)

This folder now supports a single source of truth via `.env`.

## 1) Create `.env`

```bash
cp .env.example .env
```

Edit values as needed.

## 2) One-command deploy

```bash
chmod +x deploy.sh prepare.sh healthcheck.sh
./deploy.sh
```

What `deploy.sh` does:

1. Uploads scripts to all 3 nodes (`prepare.sh`)
2. Runs genesis launch on SSH port `GENESIS_SSH_PORT`
3. Waits for genesis API readiness
4. Runs join-1 and join-2 with explicit per-node env
5. Runs `healthcheck.sh`

## 3) Health checks only

```bash
./healthcheck.sh
```

## 4) Terraform + `.env`

Generate Terraform variables from `.env`:

```bash
chmod +x generate-terraform-tfvars.sh
./generate-terraform-tfvars.sh
```

Then:

```bash
terraform init
terraform plan
terraform apply
```

This keeps script-based deploys and Terraform using the same values.
