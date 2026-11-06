# Devshard v6 bounty estimation

Proposed USDT payouts for the v6 line, then the two estimation methods used as supporting analysis for package-sized contributors.

**Lead roles.** Alexander Kuprin leads **devshard**, Daniil Yankouski leads **gateway**, Anatoly Kuznetsov leads **testing automation**. Lead work includes testing, design, and maintenance across the release — not only the commits under their own PRs — and **cannot** be measured in the same metrics and terms as other contributors. The same per-PR metrics used for others are **not** applied to these three. If Method 1 / Method 2 below were applied to the leads, their bounty payments would land about **1.5×–2.0×** higher than the payout table. Short, very localized contributions have a historical practice of being overpaid relative to those metrics; that is deliberate — strong motivation for bug-hunting and for attracting contributors.

**Shared load-test work.** A large part of open [#2089](https://github.com/gonka-ai/gonka/pull/2089) (prod-like 15m loadtest) was done by Daniil Yankouski and Anatoly Kuznetsov, not only by the PR author.

**Vested GNK (separate from USDT).** Alexander Kuprin: **30,000** vested GNK. Daniil Yankouski: **55,000** vested GNK.

---



## Proposed payout

USDT (and vested GNK where noted) for the v6 line. The table below is the proposed payout from [#1792](https://github.com/gonka-ai/gonka/pull/1792).

| GitHub          | Address                                                                     | Payout                              |
| --------------- | --------------------------------------------------------------------------- | ----------------------------------- |
| a-kuprin / akup | `gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56`                              | 20,000 USDT + **30,000 vested GNK** |
| qdanik          | `gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8`                              | 14,500 USDT + **55,000 vested GNK** |
| snevolin        | `gonka1vnupswg7qz2w5k5ax6zrp02mxmln6arnvjc87h`                              | 9,000 USDT                          |
| aikuznetsov     | `gonka1frlfyz2wtltdy47dq3w9pwc8ruvjvlthp2lh53`                              | 19,125 USDT                         |
| zpoken          | `gonka1s8zggm642e3kncy48c7vxwmeclt2wxyyd8qtdt` (joint with staaason; confirm) | mainnet upgrade                     |
| staaason        | `gonka1s8zggm642e3kncy48c7vxwmeclt2wxyyd8qtdt` (joint with zpoken; confirm)   | mainnet upgrade                     |
| cyberdelamain   | `gonka15u0r3mf6t7zsfuslusnyt7hsjrq357yumpe8st`                              | 5,000 USDT                          |
| vitaly-andr     | `gonka1uqt4hue8tljwwgdkvtthyl3n8kkkqtydyns4cm`                              | 500 USDT                            |
| redstartechno   | `gonka105ce4495mj0mwkxqeasgdzqfq5jjrfq32eza5l`                              | 300 USDT                            |
| Ryanchen911     | `gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57`                              | 500 USDT                            |
| aleksandr-cstl  | UNKNOWN — never paid on chain, ask                                          | 300 USDT                            |




**USDT subtotal (numeric rows):** 20,000 + 14,500 + 9,000 + 19,125 + 5,000 + 500 + 300 + 500 + 300 = **69,225**. zpoken and staaason are paid on the mainnet upgrade path (joint wallet). kaileido is included in qdanik.

---



## Method estimates (supporting analysis)

Two methods used to price package-sized work on `devshard-0.2.x-v6`. They calibrate localized and mid-size contributions. **They are not the payout formula for the three leads** (Kuprin / Yankouski / Kuznetsov); those payouts are set in the table above.


| Contributor              | Method 1 (complexity) | Method 2 (k × ln lines) | Proposed Payments              |
| ------------------------ | --------------------- | ----------------------- | ------------------------------ |
| a-kuprin + akup (lead)   | — (not applied)       | — (not applied)         | 20,000 USDT + 30,000vested GNK |
| qdanik + kaileido (lead) | — (not applied)       | — (not applied)         | 14,500 USDT + 55,000vested GNK |
| aikuznetsov (lead)       | — (not applied)       | — (not applied)         | 19,125 USDT                    |
| snevolin                 | 7,000                 | 8,100                   | 9,000 USDT                     |
| cyberdelamain            | 6,000                 | 4,300                   | 5,000 USDT                     |


Line columns in the PR tables below are production Go/proto and tests (`*_test.go`, test scripts, testdata). Docs, generated stubs, and config are excluded. Complexity is the Method 1 1–5 mark. Method 2 folds follow-ups into the named package rather than paying each PR alone.

---



## Merged PRs by contributor

Base branch `devshard-0.2.x-v6`, unless noted. Closed-unmerged and open PRs are excluded.

### a-kuprin + akup


| PR                                                   | Production | Test       | Complexity | Method 2 package              | Description                                                                                       |
| ---------------------------------------------------- | ---------- | ---------- | ---------- | ----------------------------- | ------------------------------------------------------------------------------------------------- |
| [#1832](https://github.com/gonka-ai/gonka/pull/1832) | 18,531     | 26,596     | 5          | Peer RPC and its caps         | Peer RPC over Connect and HTTP/2                                                                  |
| [#2030](https://github.com/gonka-ai/gonka/pull/2030) | 947        | 1,654      | 4          | Peer RPC and its caps         | Bound peer RPC memory, Chat slots, and admission                                                  |
| [#2061](https://github.com/gonka-ai/gonka/pull/2061) | 405        | 1,277      | 3          | Peer RPC and its caps         | Per-address Attach cap; HTTP/2 per version off HA                                                 |
| [#2052](https://github.com/gonka-ai/gonka/pull/2052) | 14         | 271        | 2          | Peer RPC and its caps         | Cap concurrent peer RPC connections per source                                                    |
| [#1930](https://github.com/gonka-ai/gonka/pull/1930) | 1,267      | 3,126      | 5          | Live-set hash as a curve sum  | Stop hashing/copying the full live set (Ristretto255 sum). Merged to `devshard/v5`; present on v6 |
| [#2050](https://github.com/gonka-ai/gonka/pull/2050) | 3,696      | 4,733      | 4          | Paged challenge receipts      | Page the refused-timeout ChallengeReceipt                                                         |
| [#2003](https://github.com/gonka-ai/gonka/pull/2003) | 1,466      | 1,525      | 4          | Catch-up writer               | Queue diff writes so catch-up survives a 4-connection pool                                        |
| [#1982](https://github.com/gonka-ai/gonka/pull/1982) | 624        | 248        | 3          | Catch-up writer               | Reclaim owed stale leases after a failed catch-up                                                 |
| [#1993](https://github.com/gonka-ai/gonka/pull/1993) | 27         | 82         | 3          | Catch-up writer               | Require a state hash before catch-up skips ahead                                                  |
| [#1568](https://github.com/gonka-ai/gonka/pull/1568) | 5,346      | 2,301      | 5          | Gateway Postgres              | Gateway v6 Postgres, fail-closed                                                                  |
| [#1558](https://github.com/gonka-ai/gonka/pull/1558) | 6,999      | 6,829      | 3          | Other rules and the e2e suite | Observability e2e suite                                                                           |
| [#2031](https://github.com/gonka-ai/gonka/pull/2031) | 799        | 1,166      | 3          | Other rules and the e2e suite | Open a session while a neighbour URL is still unknown                                             |
| [#1945](https://github.com/gonka-ai/gonka/pull/1945) | 547        | 850        | 3          | Other rules and the e2e suite | Sequence an executor finish that rejects an execution timeout                                     |
| [#1791](https://github.com/gonka-ai/gonka/pull/1791) | 358        | 697        | 3          | Other rules and the e2e suite | Unify host signature identity                                                                     |
| [#1977](https://github.com/gonka-ai/gonka/pull/1977) | 238        | 343        | 3          | Other rules and the e2e suite | Count late Phase-A votes; reclaim Phase-B leases                                                  |
| [#2001](https://github.com/gonka-ai/gonka/pull/2001) | 32         | 188        | 3          | Other rules and the e2e suite | Do not re-verify a ConfirmStart after it is settled                                               |
| [#1663](https://github.com/gonka-ai/gonka/pull/1663) | 192        | 214        | 2          | Other rules and the e2e suite | Grow readiness window for slow starts, not hung ones                                              |
| [#1983](https://github.com/gonka-ai/gonka/pull/1983) | 200        | 123        | 2          | Other rules and the e2e suite | Bound race-cleanup join after cancel                                                              |
| [#2007](https://github.com/gonka-ai/gonka/pull/2007) | 126        | 325        | 2          | Other rules and the e2e suite | Retry a payload 429 once, then vote false                                                         |
| [#1962](https://github.com/gonka-ai/gonka/pull/1962) | 77         | 120        | 2          | Other rules and the e2e suite | Disable height-sync heartbeats until the 0.2.18 upgrade                                           |
| [#1991](https://github.com/gonka-ai/gonka/pull/1991) | 62         | 28         | 2          | Other rules and the e2e suite | Default quiet settle to 5m                                                                        |
| [#2002](https://github.com/gonka-ai/gonka/pull/2002) | 56         | 143        | 2          | Other rules and the e2e suite | Vote false when payload Attach never completes                                                    |
| [#1974](https://github.com/gonka-ai/gonka/pull/1974) | 0          | 0          | 2          | Other rules and the e2e suite | Keep pull-request code away from the CI key (config only)                                         |
| [#1985](https://github.com/gonka-ai/gonka/pull/1985) | 181        | 224        | 1          | Other rules and the e2e suite | Keep Validate latency off Execute first token                                                     |
| **Sum (base v6)**                                    | **40,923** | **49,937** | **66**     |                               | 23 PRs with base `devshard-0.2.x-v6`                                                              |
| **Sum (+ #1930)**                                    | **42,190** | **53,063** | **71**     |                               | Method 2 only: #1930 is a separate package because the curve-sum code is on the v6 tree           |




### qdanik + kaileido


| PR                                                   | Production | Test       | Complexity | Method 2 package        | Description                                                    |
| ---------------------------------------------------- | ---------- | ---------- | ---------- | ----------------------- | -------------------------------------------------------------- |
| [#1972](https://github.com/gonka-ai/gonka/pull/1972) | 702        | 1,993      | 5          | Short-output validation | Short-output validation without the always-on min_tokens floor |
| [#1829](https://github.com/gonka-ai/gonka/pull/1829) | 978        | 2,112      | 5          | `served_hash`           | Bind the gateway stream to Finish with a signed `served_hash`  |
| [#1882](https://github.com/gonka-ai/gonka/pull/1882) | 1,847      | 3,209      | 4          | Gateway v5 onto v6      | Bring gateway v5 onto v6 (finalize, holds, ports)              |
| [#1964](https://github.com/gonka-ai/gonka/pull/1964) | 1,506      | 682        | 3          | Other gateway changes   | PerfStore with SQLite and Postgres backends                    |
| [#1988](https://github.com/gonka-ai/gonka/pull/1988) | 610        | 811        | 3          | Other gateway changes   | Review fixes for the v6 release                                |
| [#1816](https://github.com/gonka-ai/gonka/pull/1816) | 414        | 897        | 3          | Other gateway changes   | Logprobs optimization off by default, overridable              |
| [#1817](https://github.com/gonka-ai/gonka/pull/1817) | 384        | 1,481      | 3          | Other gateway changes   | Route chat only to an escrow whose balance covers it           |
| [#1781](https://github.com/gonka-ai/gonka/pull/1781) | 317        | 1,150      | 3          | Other gateway changes   | Gateway v4.1.1 backmerge onto v6                               |
| [#1739](https://github.com/gonka-ai/gonka/pull/1739) | 273        | 751        | 3          | Other gateway changes   | Gzip the inference answer and accept a gzipped request         |
| [#2080](https://github.com/gonka-ai/gonka/pull/2080) | 272        | 433        | 3          | Other gateway changes   | v6 fixes part 2                                                |
| [#1873](https://github.com/gonka-ai/gonka/pull/1873) | 240        | 401        | 3          | Other gateway changes   | Reject out-of-vocab enforced tokens before replay              |
| [#1866](https://github.com/gonka-ai/gonka/pull/1866) | 29         | 140        | 2          | Other gateway changes   | Per-model escrow settlement                                    |
| [#1975](https://github.com/gonka-ai/gonka/pull/1975) | 4          | 27         | 2          | Other gateway changes   | Select pooled runtime by resolved model                        |
| [#1987](https://github.com/gonka-ai/gonka/pull/1987) | 5          | 40         | 1          | Other gateway changes   | Spell mock top_logprobs from the token id                      |
| [#1916](https://github.com/gonka-ai/gonka/pull/1916) | 2          | 2          | 1          | Other gateway changes   | Decline when a refused-timeout verify has no payload           |
| **Sum**                                              | **7,583**  | **14,129** | **44**     |                         |                                                                |




### snevolin


| PR                                                   | Production | Test      | Complexity | Method 2 package              | Description                                                |
| ---------------------------------------------------- | ---------- | --------- | ---------- | ----------------------------- | ---------------------------------------------------------- |
| [#2051](https://github.com/gonka-ai/gonka/pull/2051) | 626        | 2,207     | 4          | Port, fallback, and soft-stop | Peer RPC on InferenceUrl with HTTP/2 and HTTP/1.1 fallback |
| [#2041](https://github.com/gonka-ai/gonka/pull/2041) | 0          | 569       | 2          | Port, fallback, and soft-stop | HAProxy soft-stop; drop h2-watch-drain supervisor          |
| [#1744](https://github.com/gonka-ai/gonka/pull/1744) | 0          | 0         | 1          | (docs; L = 0)                 | HA setup follow-up after review (docs only)                |
| [#2109](https://github.com/gonka-ai/gonka/pull/2109) | 0          | 0         | 1          | (docs; L = 0)                 | Sync HA host installation guide for v6 (docs only)         |
| Review                                               | —          | —         | —          | Flat add-on                   | 2,000 USDT                                                 |
| **Sum**                                              | **626**    | **2,776** | **8**      |                               | Method 1: 4k + 1k + 2k review = 7,000                      |




### cyberdelamain


| PR                                                   | Production | Test      | Complexity | Method 2 package | Description                                                      |
| ---------------------------------------------------- | ---------- | --------- | ---------- | ---------------- | ---------------------------------------------------------------- |
| [#1902](https://github.com/gonka-ai/gonka/pull/1902) | 111        | 274       | 3          | The v6 fixes     | Commit stored payload hashes when another execution stored first |
| [#2023](https://github.com/gonka-ai/gonka/pull/2023) | 41         | 61        | 3          | The v6 fixes     | Read a lagging host’s catch-up diffs outside Session.mu          |
| [#2077](https://github.com/gonka-ai/gonka/pull/2077) | 136        | 266       | 2          | The v6 fixes     | Retry a refused Attach, then vote false                          |
| [#2094](https://github.com/gonka-ai/gonka/pull/2094) | 26         | 176       | 2          | The v6 fixes     | Vote false when GetPayload drops and re-Attach stalls            |
| [#1862](https://github.com/gonka-ai/gonka/pull/1862) | 63         | 51        | 2          | The v6 fixes     | Strip internal fields inside the fold                            |
| [#1893](https://github.com/gonka-ai/gonka/pull/1893) | 16         | 47        | 2          | The v6 fixes     | Fail startup when bootstrap GetCurrentEpoch fails                |
| [#1907](https://github.com/gonka-ai/gonka/pull/1907) | 10         | 42        | 2          | The v6 fixes     | Vote false on a payload 404 in the PoC window                    |
| [#1856](https://github.com/gonka-ai/gonka/pull/1856) | 6          | 91        | 2          | The v6 fixes     | Do not keep a stale validation lease after a failed catch-up     |
| [#1854](https://github.com/gonka-ai/gonka/pull/1854) | 0          | 28        | 1          | The v6 fixes     | Wait for Phase B in the fetch-failure verdict test               |
| Review                                               | —          | —         | —          | Flat add-on      | 2,000 USDT                                                       |
| **Sum**                                              | **409**    | **1,036** | **19**     |                  | Method 1: 4k fixes + 2k review = 6,000                           |


---



## Method 1 — Complexity and overall estimation

Calibrate against historical USDT bounties (0.2.12–0.2.16 and the v5 proposal text), excluding the contributor being priced. Do not scale by raw lines. Group work into designed packages and assign a 1–5 complexity mark, then price from the record of what similar packages were paid.

### Complexity scale


| Score | Meaning                                                                                   |
| ----- | ----------------------------------------------------------------------------------------- |
| 1     | One-function bug or test-only                                                             |
| 2     | Local behavior fix                                                                        |
| 3     | Subtle multi-package invariant                                                            |
| 4     | New concurrency or memory bound; analysis and design choices during implementation        |
| 5     | New rule on money / hash / storage path, or a large feature that changes product behavior |


A backport of an already-scored design scores 2. Design intent and authorship matter: bug and task payments (≈1–7k) are the wrong scale for work where the author decided the protocol. Maintenance windows (7,500) is the main standalone designed-subsystem anchor. qdanik’s v5 proposal line (23,000) is the anchor for a designed gateway generation.

### Historical anchors used


| Band                     | USDT          | Examples                                                        |
| ------------------------ | ------------- | --------------------------------------------------------------- |
| One-function / small fix | 500–1,000     | ouicate chain fixes, small 0xMayoor settle fixes                |
| Safety / medium finding  | 2,000–3,000   | fund atomicity, DKG, acquire-node gRPC, reward-claim recipients |
| Designed subsystem       | 7,000–7,500   | maintenance windows, pixelplex’s four-PR package                |
| Designed generation      | 12,000–23,000 | snevolin HA (v5 text), qdanik validation/gateway (v5 text)      |
| Flat review              | 1,000–2,500   | upgrade reviews                                                 |




### Aggregated result


| Contributor       | Complexity sum (merged PRs) | Design framing                                                                | Bounty (USDT) |
| ----------------- | --------------------------- | ----------------------------------------------------------------------------- | ------------- |
| a-kuprin + akup   | 66                          | Peer RPC as designed subsystem (~15k) + escrow/gateway generation (~23k)      | 38,000        |
| qdanik + kaileido | 44                          | `served_hash` + short-output (~6k each) + remaining gateway ports/fixes (~6k) | 18,000        |
| snevolin          | 8                           | Port/fallback feature (~4k) + soft-stop (~1k) + review (2k)                   | 7,000         |
| cyberdelamain     | 19                          | Three bug-fix rows (~4k) + review (2k)                                        | 6,000         |
| **Total**         |                             |                                                                               | **69,000**    |




### Package breakdown (Method 1)


| Contributor   | Package                                                                | Complexity | Bounty |
| ------------- | ---------------------------------------------------------------------- | ---------- | ------ |
| a-kuprin      | Peer RPC (+ admission / caps follow-ups)                               | 5          | 15,000 |
| a-kuprin      | Escrow / gateway generation (Postgres, receipts, catch-up, rules, e2e) | 3–5 set    | 23,000 |
| qdanik        | `served_hash`                                                          | 5          | 6,000  |
| qdanik        | Short-output validation                                                | 5          | 6,000  |
| qdanik        | Remaining gateway features, ports, review fixes                        | 2–4 set    | 6,000  |
| snevolin      | Peer RPC on InferenceUrl + H2/H1 fallback                              | 4          | 4,000  |
| snevolin      | HAProxy soft-stop                                                      | 2          | 1,000  |
| snevolin      | Review                                                                 | —          | 2,000  |
| cyberdelamain | Payload / vote-false fixes                                             | 2–3        | 2,000  |
| cyberdelamain | Catch-up lock / stale lease                                            | 2–3        | 1,000  |
| cyberdelamain | Startup / fold / tests                                                 | 1–2        | 1,000  |
| cyberdelamain | Review                                                                 | —          | 2,000  |


---



## Method 2 — k score and ln(lines)

Score each **design package** (not each PR) with a human-effort weight `k`, then scale by a log of weighted lines so size matters less as a change grows.

### Line weight

```
L = production + 0.5 × test
```

Production = Go + proto (excluding tests and generated stubs). Tests = `*_test.go`, test scripts, testdata. Docs / config / generated stubs are excluded from `L`.

### Formula

```
bounty = 5,250 × k × ln(1 + L) / ln(101)
```

`ln(101)` is the reference size of a 100-line weighted change. At `k = 1`:


| Weighted lines L | Bounty  |
| ---------------- | ------- |
| 50               | ~4,500  |
| 100              | 5,250   |
| 1,000            | ~7,900  |
| 3,000            | ~9,100  |
| 10,000           | ~10,500 |
| 30,000           | ~11,700 |


Rate `5,250` was fit to executed USDT bounties that name PRs (0.2.12–0.2.16). GNK-era 0.2.11 confirms the same ladder shape but is not mixed into the USDT fit. v2 has no bounty table.

### k scale (human added value)


| k    | Meaning                                                                                               |
| ---- | ----------------------------------------------------------------------------------------------------- |
| 0.1  | Local bug / agent-closable; human mainly reviews the result                                           |
| 0.3  | Human must state the intended behavior; implementation then ordinary                                  |
| 0.7  | Human finds the failure, compares fixes, and chooses during implementation                            |
| 1.0  | Large feature that changes product behavior; sustained human control of agentic work and hard testing |
| +0.2 | Design proposal authored by the same person, applied when base `k > 0.7`                              |


If the GitHub issue was filed by someone else and already specifies what to build, the design add is **0** (example: `served_hash` implements #1714 filed by a-kuprin → stays at 1.0).

Reviews stay outside the formula as a flat add-on.

### Aggregated result


| Contributor       | Code       | Tests      | Packages   | Bounty (USDT) |
| ----------------- | ---------- | ---------- | ---------- | ------------- |
| a-kuprin + akup   | 42,190     | 53,063     | 6          | 44,800        |
| qdanik + kaileido | 7,583      | 14,129     | 4          | 28,400        |
| snevolin          | 626        | 2,776      | 1 + review | 8,100         |
| cyberdelamain     | 409        | 1,036      | 1 + review | 4,300         |
| **Total**         | **50,808** | **71,004** |            | **85,600**    |




### Package breakdown (Method 2)


| Package                                                             | k   | Code       | ln(1+code) | Tests      | ln(1+tests) | L      | ln(1+L) | Bounty     |
| ------------------------------------------------------------------- | --- | ---------- | ---------- | ---------- | ----------- | ------ | ------- | ---------- |
| a-kuprin, peer RPC and its caps                                     | 1.2 | 19,897     | 9.90       | 29,798     | 10.30       | 34,796 | 10.46   | 14,300     |
| a-kuprin, live-set hash as a curve sum (#1930 on v5, present on v6) | 1.2 | 1,267      | 7.15       | 3,126      | 8.05        | 2,830  | 7.95    | 10,900     |
| a-kuprin, paged challenge receipts                                  | 0.7 | 3,696      | 8.22       | 4,733      | 8.46        | 6,062  | 8.71    | 6,900      |
| a-kuprin, catch-up writer                                           | 0.7 | 2,117      | 7.66       | 1,855      | 7.53        | 3,044  | 8.02    | 6,400      |
| a-kuprin, gateway Postgres                                          | 0.3 | 5,346      | 8.58       | 2,301      | 7.74        | 6,496  | 8.78    | 3,000      |
| a-kuprin, other rules and the e2e suite                             | 0.3 | 9,867      | 9.20       | 11,250     | 9.33        | 15,492 | 9.65    | 3,300      |
| **a-kuprin v6**                                                     |     | **42,190** |            | **53,063** |             |        |         | **44,800** |
| qdanik, short-output validation                                     | 1.2 | 702        | 6.56       | 1,993      | 7.60        | 1,698  | 7.44    | 10,200     |
| qdanik, `served_hash`                                               | 1.0 | 978        | 6.89       | 2,112      | 7.66        | 2,034  | 7.62    | 8,700      |
| qdanik, gateway v5 onto v6                                          | 0.7 | 1,847      | 7.52       | 3,209      | 8.07        | 3,452  | 8.15    | 6,500      |
| qdanik, other gateway changes                                       | 0.3 | 4,056      | 8.31       | 6,815      | 8.83        | 7,464  | 8.92    | 3,000      |
| **qdanik v6**                                                       |     | **7,583**  |            | **14,129** |             |        |         | **28,400** |
| snevolin, port, fallback, and soft-stop                             | 0.7 | 626        | 6.44       | 2,776      | 7.93        | 2,014  | 7.61    | 6,100      |
| snevolin, review                                                    | —   |            |            |            |             |        |         | 2,000      |
| **snevolin v6**                                                     |     | **626**    |            | **2,776**  |             |        |         | **8,100**  |
| cyberdelamain, the v6 fixes                                         | 0.3 | 409        | 6.02       | 1,036      | 6.94        | 927    | 6.83    | 2,300      |
| cyberdelamain, review                                               | —   |            |            |            |             |        |         | 2,000      |
| **cyberdelamain v6**                                                |     | **409**    |            | **1,036**  |             |        |         | **4,300**  |


---



## Method comparison notes

- **Method 1** prices by historical package class (subsystem vs generation vs bug row). Lines are context only. It stays closer to prior proposal totals for a full generation.
- **Method 2** makes size explicit via `ln(1+L)` and separates human design effort in `k`. It raises packages that are both large and high-`k` (peer RPC, curve-sum hash, short-output) relative to Method 1’s lump for qdanik’s generation.
- Both methods bill a design once: follow-up PRs are folded into the parent package rather than scored as independent 0.3 rows.
- Linear `lines × k` was tried and rejected: it overpaid peer RPC by an order of magnitude versus every paid large feature in the record.
- **Leads are outside both methods.** Kuprin (devshard), Yankouski (gateway), and Kuznetsov (testing automation) are paid from the proposed payout table, not from these scores. Their work includes testing, design, and maintenance for the release as a whole, and cannot be measured in the same metrics and terms as other contributors. Applying the methods to them would raise their USDT by roughly 1.5×–2.0×.
- **Localized contributions are historically overpaid** relative to the same metrics. That overpay is intentional: it keeps bug-hunting and new contributors motivated.
- **#2089** (prod-like loadtest, open under a-kuprin) contains a large amount of work by Yankouski and Kuznetsov; authorship of that PR alone understates their share.

