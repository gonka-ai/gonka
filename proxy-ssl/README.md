## proxy-ssl

### What this service is for

`proxy-ssl` is a small HTTP service that issues TLS certificates via ACME DNS-01 (Let’s Encrypt) for subdomains of a single base domain. It is used in Gonka deployments to automatically issue certs for hosts like `explorer.<domain>`, `api.<domain>`, `rpc.<domain>`, etc.

- **How it works**: clients submit a CSR and the desired FQDNs; the service performs DNS-01 challenges using your DNS provider credentials, then returns a certificate bundle.
- **Security**: requests must be authorized with a JWT (`CERT_ISSUER_JWT_SECRET`). Only subdomains listed in `CERT_ISSUER_ALLOWED_SUBDOMAINS` under `CERT_ISSUER_DOMAIN` are allowed.
- **Storage**: issued bundles are written under `cert_storage_path` (default `/app/certs`).
- **Providers**: Route53, Cloudflare, Google Cloud DNS, Azure DNS, DigitalOcean DNS, Hetzner DNS.

If configuration is missing/invalid, the container runs in a disabled mode and serves only `/health` for liveness checks.

### Quick start (docker-compose)

Enable with the ssl profile and restart only the proxy services:

```bash
source config.env && \
docker compose pull proxy proxy-ssl && \
docker compose --profile "ssl" \
  -f docker-compose.mlnode.yml \
  -f docker-compose.yml \
  up -d proxy proxy-ssl
```

Notes:
- Only `proxy` and `proxy-ssl` need to be started/restarted when enabling SSL or updating their env.
- The rest of the stack can keep running unchanged.

### Required environment variables

- **ACME configuration**
  - `ACME_ACCOUNT_EMAIL` (required): Email for Let’s Encrypt account.
  - `ACME_DNS_PROVIDER` (required): One of `route53`, `cloudflare`, `gcloud`, `azure`, `digitalocean`, `hetzner`.
  - `ACME_ENV` (optional): `staging` to use LE staging directory; otherwise production is used.

- **Service configuration**
  - `CERT_ISSUER_DOMAIN` (required): Base domain (e.g., `gonka.ai`).
  - `CERT_ISSUER_ALLOWED_SUBDOMAINS` (required): Comma-separated list (e.g., `explorer,api,rpc`).
  - `CERT_ISSUER_JWT_SECRET` (required): Secret for request authentication.
  - `PORT` (optional): Port to bind (defaults to `8080`).
  - `CERT_STORAGE_PATH` (optional): Where to store issued bundles (defaults to `/app/certs`).
  - `DATA_PATH` (optional): General data path (defaults to `/app/data`).

- **DNS provider credentials** (see per-provider guides below)

### DNS provider credentials

Below are step-by-step instructions to obtain credentials for each supported provider.

#### Azure DNS

Option A — Azure CLI (quick + least clicks)

```bash
# 1) Login and pick your subscription
az login
az account set --subscription "<your-subscription-name-or-id>"

# 2) Set where your DNS zone lives
RG="<<your-dns-resource-group>>"
ZONE="<<your-zone>>"         # e.g., gonka.ai
SP_NAME="gonka-acme-$(date +%s)"

SUBSCRIPTION_ID=$(az account show --query id -o tsv)
SCOPE="/subscriptions/$SUBSCRIPTION_ID/resourceGroups/$RG/providers/Microsoft.Network/dnszones/$ZONE"

CREDS=$(az ad sp create-for-rbac \
  --name "$SP_NAME" \
  --role "DNS Zone Contributor" \
  --scopes "$SCOPE" \
  --only-show-errors)

# 4) Extract the values you need
AZURE_CLIENT_ID=$(echo "$CREDS" | jq -r .appId)
AZURE_CLIENT_SECRET=$(echo "$CREDS" | jq -r .password)
AZURE_TENANT_ID=$(echo "$CREDS" | jq -r .tenant)

# 5) Print them (and the subscription id) to copy into your env file
echo "AZURE_CLIENT_ID=$AZURE_CLIENT_ID"
echo "AZURE_CLIENT_SECRET=$AZURE_CLIENT_SECRET"
echo "AZURE_SUBSCRIPTION_ID=$SUBSCRIPTION_ID"
echo "AZURE_TENANT_ID=$AZURE_TENANT_ID"
```

Option B — Portal clicks (no CLI)

1. DNS zone must be hosted in Azure DNS. (If your domain uses Cloudflare/Route53 nameservers, use that provider instead.)
2. Go to Microsoft Entra ID → App registrations → New registration → name it (e.g., gonka-acme) → Register.
   - The Application (client) ID on the Overview page = `AZURE_CLIENT_ID`.
   - The Directory (tenant) ID = `AZURE_TENANT_ID`.
3. In the app: Certificates & secrets → New client secret → copy the value = `AZURE_CLIENT_SECRET` (you won’t see it again).
4. Go to Subscriptions → pick the subscription → copy Subscription ID = `AZURE_SUBSCRIPTION_ID`.
5. Assign permissions to the zone:
   - Open your DNS zone (Resource Group → your zone).
   - Access control (IAM) → Add role assignment → Role: DNS Zone Contributor → Members: the app you created.

#### AWS Route53

Option A — AWS CLI

Prereqs: `aws` CLI configured; you know your Hosted Zone ID and region.

```bash
# 1) Create a least-privilege policy scoped to your hosted zone:
HOSTED_ZONE_ID="Z123EXAMPLE"
cat > route53-acme.json <<'JSON'
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "route53:ChangeResourceRecordSets"
      ],
      "Resource": "arn:aws:route53:::hostedzone/${HOSTED_ZONE_ID}"
    },
    {
      "Effect": "Allow",
      "Action": [
        "route53:ListHostedZones",
        "route53:ListHostedZonesByName",
        "route53:ListResourceRecordSets",
        "route53:GetChange"
      ],
      "Resource": "*"
    }
  ]
}
JSON

aws iam create-policy \
  --policy-name acme-dns-route53-${HOSTED_ZONE_ID} \
  --policy-document file://route53-acme.json | jq -r .Policy.Arn

# 2) Create an IAM user and access keys, then attach the policy:

USER_NAME="acme-dns"
POLICY_ARN=$(aws iam list-policies --query "Policies[?PolicyName=='acme-dns-route53-${HOSTED_ZONE_ID}'].Arn" -o tsv)

aws iam create-user --user-name "$USER_NAME" >/dev/null || true
aws iam attach-user-policy --user-name "$USER_NAME" --policy-arn "$POLICY_ARN"

CREDS=$(aws iam create-access-key --user-name "$USER_NAME")
AWS_ACCESS_KEY_ID=$(echo "$CREDS" | jq -r .AccessKey.AccessKeyId)
AWS_SECRET_ACCESS_KEY=$(echo "$CREDS" | jq -r .AccessKey.SecretAccessKey)

echo "AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID"
echo "AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY"
echo "AWS_REGION=<your-aws-region>"
```

Option B — AWS Console

1. IAM → Policies → Create policy → JSON: allow Route53 record changes for your hosted zone; save.
2. IAM → Users → Create user (programmatic access) → Attach the policy from step 1.
3. After user creation, create access key → copy `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`.
4. Set `AWS_REGION` to your region (e.g., `us-east-1`).

#### Google Cloud DNS

Option A — gcloud CLI

```bash
PROJECT_ID="<your-gcp-project>"
SA_NAME="acme-dns"
SA_EMAIL="$SA_NAME@$PROJECT_ID.iam.gserviceaccount.com"

gcloud config set project "$PROJECT_ID"

# 1) Create service account
gcloud iam service-accounts create "$SA_NAME" \
  --display-name "ACME DNS for proxy-ssl"

# 2) Grant DNS Admin (least privilege for DNS zone changes)
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member "serviceAccount:$SA_EMAIL" \
  --role "roles/dns.admin"

# 3) Create a key and base64 it for the env var
gcloud iam service-accounts keys create key.json --iam-account "$SA_EMAIL"
GCE_SERVICE_ACCOUNT_JSON_B64=$(base64 < key.json | tr -d '\n')

echo "GCE_PROJECT=$PROJECT_ID"
echo "GCE_SERVICE_ACCOUNT_JSON_B64=$GCE_SERVICE_ACCOUNT_JSON_B64"
```

Option B — Cloud Console

1. IAM & Admin → Service Accounts → Create service account (e.g., acme-dns).
2. Grant role: DNS Administrator (`roles/dns.admin`).
3. Service account → Keys → Add key → Create new key (JSON) → download.
4. Base64-encode the JSON and set `GCE_SERVICE_ACCOUNT_JSON_B64`. Set `GCE_PROJECT` to your project ID.

#### Cloudflare DNS

Option A — Portal

1. Log in to Cloudflare Dashboard.
2. Go to your Profile:
   - Click your avatar (top-right).
   - Select My Profile.
3. Open API Tokens:
   - In the left sidebar, click API Tokens.
   - You will see two sections: API Tokens and API Keys.
4. Click Create Token:
   - Choose the Edit zone DNS template (recommended), or Create Custom Token.
5. Set Permissions (for custom token):
   - Zone → Zone → Read
   - Zone → DNS → Edit
6. Limit Resources:
   - Under Zone Resources, select your specific zone (domain) instead of All zones.
7. Create & Copy:
   - Continue to summary → Create Token.
   - Copy the token immediately; Cloudflare shows it only once.
   - Set `CF_DNS_API_TOKEN`

#### DigitalOcean DNS

Option A — Portal

1. Control Panel → API → Tokens → Generate New Token.
2. Give it a descriptive name and scope with write permissions.
3. Copy the token and set `DO_AUTH_TOKEN`.

Note: Personal access tokens are created in the UI; the `doctl` CLI does not create new tokens.

#### Hetzner DNS

Option A — Portal

1. Go to `https://dns.hetzner.com` → API Tokens → New Token.
2. Give it a descriptive name, scope to required zones if applicable.
3. Copy the token and set `HETZNER_API_KEY`.

### Notes and tips

- Scope credentials to the specific DNS zone whenever possible.
- Rotate and store secrets in a secure manager. Avoid committing secrets to git.
- For Google Cloud, ensure the base64 string is a single line with no newlines.
- For AWS, consider IAM roles with OIDC instead of long-lived access keys where possible.
- Validation rules are enforced in `internal/config/config.go`.


