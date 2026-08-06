# Host stack - latest state (mainnet)

**As of:** 2026-08-06 (UTC)  
**Purpose:** Single snapshot of containers and in-container binaries

**Sources for this snapshot:**
- Live: `http://node1.gonka.ai:8000` (`abci_info`, `/v1/versions`, chain params)
- Repo pins: `deploy/join/docker-compose.yml`, `deploy/join/docker-compose.mlnode.yml`
- Announcements: [gonka.ai/docs/network-updates](https://gonka.ai/docs/network-updates/) (through 2026-08-03)

> **Image tag ≠ running binary.**  
> `node` and `api` images may say `0.2.15` / `0.2.15-post3`, but Cosmovisor `current` decides what actually runs.  
> `versiond` image is separate from the `devshardd` binaries it downloads from on-chain `approved_versions`.

---



## 1. Docker containers


| Container   | Image (expected)                                            | Required?                | What it runs                                      |
| ----------- | ----------------------------------------------------------- | ------------------------ | ------------------------------------------------- |
| `tmkms`     | `ghcr.io/product-science/tmkms-softsign-with-keygen:0.2.15` | Yes (validator)          | Softsign TMKMS                                    |
| `node`      | `ghcr.io/product-science/inferenced:0.2.15`                 | Yes                      | Chain node; **binary via Cosmovisor** (below)     |
| `api`       | `ghcr.io/product-science/api:0.2.15-post3`                  | Yes                      | DAPI; **binary via Cosmovisor** (below)           |
| `edge-api`  | `ghcr.io/product-science/edge-api:0.2.15`                   | Yes                      | Edge query API                                    |
| `versiond`  | `ghcr.io/product-science/versiond:0.2.15`                   | Yes                      | Supervises **devshardd** children from governance |
| `bridge`    | `ghcr.io/product-science/bridge:0.2.15`                     | Yes if bridging          | Ethereum EL/CL + bridge sidecar                   |
| `proxy`     | `ghcr.io/product-science/proxy:0.2.15`                      | Yes                      | Public nginx front door (`:8000`)                 |
| `proxy-ssl` | `ghcr.io/product-science/proxy-ssl:0.2.15`                  | Optional (`ssl` profile) | ACME / TLS helper                                 |
| `explorer`  | `ghcr.io/product-science/explorer:latest`                   | Optional                 | Local explorer UI                                 |
| `mlnode-*`  | `ghcr.io/gonka-ai/mlnode:3.0.14-post2`                      | Yes                      | POC participatino and inference serving           |
| `nginx`     | `nginx:1.28.0`                                              | Yes                      | Routing for mlnodes                               |


---



## 2. Binaries updated *inside* containers


| Item | Current expected |
| ---- | ---------------- |
| node | `v0.2.15`        |
| api  | `v0.2.15-post3`  |




## 3. Devshard runtimes (`versiond` → downloaded `devshardd`)


| Runtime name | Route prefix       | Binary URL  |
| ------------ | ------------------ | ----------- |
| `v3`         | `/devshard/v3/...` | [release/v0.2.13-devshard-v3.0.0/devshardd.zip](https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.13-devshard-v3.0.0/devshardd.zip) |
| `v4`         | `/devshard/v4/...` | [release/v0.2.15-devshard-v4.0.1/devshardd.zip](https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.15-devshard-v4.0.1/devshardd.zip) |


- **v1 / v2** removed from `approved_versions` (earlier governance).

