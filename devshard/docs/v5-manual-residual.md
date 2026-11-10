# v5 manual tests: residual (blocks 3, 5, 9)

Operator leftovers from [`v5-merged-features-test-plan.md`](./v5-merged-features-test-plan.md) that are **not** height-sync and are **not** waiting on another author.

| Block | Feature | Why it is here |
| ----- | ------- | -------------- |
| 3 | Warm cutover and session recovery | Citest pins boot + SHA swap on an empty journal. Confirm the same contract on a real host (status vs `recovery_complete`, timeout, snapshot, epoch prune). |
| 5 | Host ping observability | Observability-only probes. Confirm metrics, kill switch, and no routing effect on a live gateway. |
| 9 | Proxy TLS + rotation protocol stamp | Atomic cert install and `v5` → protocol `5` (not `vv5`) are join-path behaviour. |

Out of this file: block 1 ([`v5-manual-height-sync.md`](./v5-manual-height-sync.md)); block 2 (Stas); blocks 4 and 8 (Danya); blocks 6–7 and 10 (citest/unit, not an operator pass).

Join gateway: `http://127.0.0.1:18080`. Admin Bearer: `DEVSHARD_ADMIN_API_KEY`.

```bash
GATEWAY=http://127.0.0.1:18080
ADMIN_KEY=sk-admin-your-key
VERSION=v5
```

---

## 3. Warm cutover and session recovery

Overlap SHA swap publishes the new child only after `recovery_complete: true` (backlog drained **and** sealed-index / validation-obs repairs). Solo boot publishes on `/ready` **200** within seconds so a long journal is not killed by `VERSIOND_READY_TIMEOUT=60s`. Snapshot restore gap-fills the sealed index (no wipe). Epoch prune evicts in-memory hosts so a pruned escrow cannot keep serving.

`VERSIOND_RECOVERY_TIMEOUT` defaults to **30m**. It is **not** the 60s ready timeout — reusing `60s` aborts every overlap swap.

The child admin `/ready` is loopback inside `versiond` on a dynamic port. Public proof is `/{version}/healthz` 200 + chat. To read the body, exec into the versiond container and curl the child admin (or scrape `devshardd_session_recovery` / the `/ready` JSON if you have the admin port).

```gherkin
Feature: Warm cutover and session recovery

  Scenario: Solo boot serves before recovery finishes
    Given a v5 child with a journal
    When the process starts
    Then admin GET /ready is HTTP 200 within seconds
    And the body field recovery_complete is false, then true
    And sessions_pending trends to zero
    And public GET /v5/healthz is 200
    And chat returns 200 without waiting for the backlog

  Scenario: Overlap SHA swap waits for warm
    Given an HA Postgres pair and a healthy old generation
    When governance publishes a new SHA for the same version name
    Then versiond logs "warm cutover: new child recovery complete"
      before "swapped child route; old child draining"
    And new chat still returns 200

  Scenario: Warm wait timeout does not cause an outage
    Given VERSIOND_RECOVERY_TIMEOUT shorter than recovery of this journal
    When the overlap swap waits
    Then versiond logs "warm-cutover wait timed out; old child keeps serving"
    And chat still returns 200 on the old child

  Scenario: Solo restart does not wait for warm
    Given a single host with no healthy old generation
    When that host restarts
    Then it rejoins the pool on /ready status 200
    And it does not wait out the recovery backlog before serving

  Scenario: Snapshot restore keeps sealed inferences
    Given a host that restored from snapshot
    When chat continues
    Then previously sealed inferences still query
    And a duplicate sealed mutation is rejected

  Scenario: Epoch prune drops in-memory hosts
    Given an escrow that is pruned at epoch change
    Then that escrow stops serving
    And no ghost in-memory host answers chat for it
```

**Do this**

1. Boot / restart one v5 host. Hit versiond `GET /$VERSION/healthz` (join router or host). It must be **200 within seconds**.
2. If you can reach admin `/ready`: `curl -s <admin>/ready | jq '{status: . , recovery_complete, sessions_pending}'` — status 200 immediately; `recovery_complete` flips `false` → `true`; `sessions_pending` trends to 0.
3. Same-name SHA swap (HA Postgres pair only). In `versiond` logs, `warm cutover: new child recovery complete` **before** `swapped child route; old child draining`. Then one chat 200.
4. If you see `warm-cutover wait timed out; old child keeps serving`: that is a **pass for no-outage** (raise `VERSIOND_RECOVERY_TIMEOUT` and let the next reconcile retry). Chat must still 200 on the old child.
5. Solo restart (no healthy old generation): host rejoins on status 200; do **not** wait the full backlog before `/healthz` 200.
6. After a snapshot restart: query a previously sealed inference; a duplicate sealed mutation still rejects.
7. On epoch change: a pruned escrow stops serving (no ghost in-memory host).

Citest companion (empty journal, not a substitute for a long-journal host): `make -C devshard/testenv citest-versiond-warm-cutover`.

**Pass:** `/ready` 200 quickly; overlap swap waits for warm then serves; timeout leaves the old child up; solo restart is not gated on the body; snapshot does not wipe sealed rows; pruned escrows disappear.

---

## 5. Host ping observability

[#1580](https://github.com/gonka-ai/gonka/pull/1580). Two probe jobs, **observability only** (no routing, no quarantine, no PerfTracker). Gateway pings **used** escrow hosts (`GET {prefix}/clock`, fallback `/healthz` + `Date`). Dapi pings ML-node inventory. A probe outage must not 429 chat.

Defaults: interval **15s**, timeout **2s**, concurrency **8**. Leave the four `DEVSHARD_GATEWAY_HOST_PING_*` vars unset unless you need a kill switch or a faster scrape.

```gherkin
Feature: Host ping observability

  Scenario: Unused hosts are not probed
    Given host ping enabled at defaults
    And a registered escrow that has never inferred
    Then devshard_gateway_host_ping_up is absent for that host
    And devshard_gateway_host_ping_targets is 0 or absent

  Scenario: Used hosts appear in ping metrics
    When a chat completion succeeds
    And one ping interval elapses
    Then devshard_gateway_host_ping_up{host=…} is 1
    And devshard_gateway_host_ping_targets >= 1

  Scenario: Kill switch does not affect chat
    Given DEVSHARD_GATEWAY_HOST_PING_DISABLED=true
    When a chat completion is sent
    Then the response is HTTP 200
    And the host_ping_up series is absent

  Scenario: Probe failure does not quarantine
    Given a used host whose /clock (or fallback /healthz) fails
    When the ping job ticks
    Then chat to that participant still returns 200
    And the participant is not quarantined

  Scenario: Dapi mlnode ping is independent
    Given DAPI_API__MLNODE_PING_DISABLED is unset
    Then api:9100/metrics exposes dapi_mlnode_ping_*
```

**Do this**

1. Before any chat on a fresh escrow: `curl -sS "$GATEWAY/metrics" | grep host_ping` — no `host_ping_up` for unused hosts; `targets` 0 or absent.
2. Send one chat. Wait ≥15s (or set `DEVSHARD_GATEWAY_HOST_PING_INTERVAL=3s` for a faster scrape). Then:

```bash
curl -sS "$GATEWAY/metrics" | grep -E 'devshard_gateway_host_ping_(up|targets|rtt_seconds)'
```

Expect `host_ping_up{host=…} 1` and `host_ping_targets` ≥ 1.

3. Kill switch: set `DEVSHARD_GATEWAY_HOST_PING_DISABLED=true`, restart the gateway, chat 200, no `host_ping_up` series. Restore the default afterwards.
4. Optional: stop `/clock` (or drop the host) on one used target. Chat must still 200; the participant must **not** enter quarantine / 429.
5. Optional: scrape `http://127.0.0.1:9100/metrics` (dapi) for `dapi_mlnode_ping_*` unless `DAPI_API__MLNODE_PING_DISABLED=true`.

Citest companion: `make -C devshard/testenv citest-host-ping`.

**Pass:** only used hosts are probed; kill switch is chat-safe; a dead probe does not take the host out of routing.

---

## 9. Proxy TLS and rotation protocol stamp

[#1661](https://github.com/gonka-ai/gonka/pull/1661) writes certs to `*.tmp.$$` and `mv -f` into place so a crash cannot leave a truncated `cert.pem` live. [#1662](https://github.com/gonka-ai/gonka/pull/1662) strips a leading `v` when stamping rotation `protocol_version`: route `/devshard/v5` stamps **`5`**, not `vv5`. The HTTP route name stays `v5` (`GET /v5/healthz`, bind).

```gherkin
Feature: Proxy TLS and rotation protocol stamp

  Scenario: TLS cert install is crash-consistent
    Given a live proxy presenting a complete certificate
    When a certificate write is interrupted once
    Then the next TLS handshake still presents a complete cert
    And it is either the previous cert or the new one
    And it is never a truncated or unparseable file

  Scenario: Rotation stamp strips a leading v
    Given governance version name v5
    When the gateway creates or rotates an escrow on /devshard/v5
    Then protocol_version is 5, not vv5 and not v5
    And GET /v5/healthz is 200
    And bind against that escrow succeeds
```

**Do this — TLS**

1. Record the live cert: `echo | openssl s_client -connect <proxy-host>:443 -servername <name> 2>/dev/null | openssl x509 -noout -fingerprint -subject`
2. Trigger a proxy cert rotate / renew (join `setup-ssl` / issuer path).
3. Interrupt the writer once (SIGKILL the `setup-ssl` / sidecar process during the write, or kill the container mid-renew).
4. Handshake again. `openssl x509` must still parse. Fingerprint is the **old** or the **new** cert — never a half file. Nginx/HAProxy must not fail the handshake with a truncated PEM.
5. If you cannot time a live interrupt, run the scripted pin from the repo root: `proxy/setup-ssl_test.sh` (rename failure must leave the previous complete cert).

**Do this — protocol stamp**

1. Hit the versiond route through the join proxy / router: `GET /$VERSION/healthz`. Expect **200**.
2. Create or rotate an escrow on the v5 route (gateway rotation, or one admin escrow create that binds `v5`).
3. Read the gateway escrow record (`GET /v1/debug/state` or rotation debug with `$ADMIN_KEY`). `protocol_version` must be **`5`**. Fail if you see `vv5` or `v5` as the stamp.
4. Bind / chat against that escrow → 200. Route name `v5` and stamp `5` must agree (same escrow, no version-mismatch reject).

**Pass:** TLS handshake never serves a truncated cert; rotation stamp is `5` for governance `v5`; healthz and bind use route `v5` and succeed.
