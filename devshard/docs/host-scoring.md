# Host Scoring: Elo-driven Speculative Routing

The devshard proxy ranks runtime hosts by **how fast they end-to-end finish
inferences**, not by how fast they start them. The ranking drives
`Redundancy.Decide` — specifically, the decision to launch a *secondary*
attempt in parallel with the primary when a strictly faster candidate is
known.

This doc is the ground truth for the formula. The implementation lives in
[`host_scores.go`](../cmd/devshardctl/host_scores.go) and the wiring lives
in [`redundancy.go`](../cmd/devshardctl/redundancy.go). For an end-to-end
visual walkthrough of every behavioral scenario (H2H, score path,
ε-greedy, unresponsive primary, Layer 3 quarantine and backoff,
regime-change recovery) see [host-scoring-schemes.md](host-scoring-schemes.md).

## Problem

The legacy routing path used a TTFT-based heuristic
(`decideLegacySecondaryFaster`) to decide whether a secondary attempt would
help. In production captures we found this was wrong **44.6% of the time**:
the candidate that won on TTFT was not the one that finished first
end-to-end ([2026-05-24-stats.md](../../docs/chat-api/2026-05-24-stats.md)).
The fastest-to-first-token host was often slower to finish, because TTFT is
heavily influenced by transport latency and queue position whereas total
time is dominated by per-token throughput on the host.

Host scoring is the answer: keep a per-host running view of *total* time
(plus TTFT for richer scoring) and an Elo rating that updates on each
realised race.

## When this is consulted

Speculative-routing strategy is selected via the `RedundancySpeedPolicy`
enum (`POST /v1/admin/settings` field `redundancy.speed_policy`):

Each policy is a **self-contained branch** of the `Decide()` switch — no
cascading between policies. The chosen branch always returns a complete
`Decision` and that is the final answer.

| Policy | Primary path | On miss |
|---|---|---|
| `host_score` (default) | `decideHostScoreSpeedup` | (no miss — always returns a final `Decision`; see Reasons table) |
| `hybrid` | `decidePairwiseSpeedup` | fall back to `decideLegacySecondaryFaster` (TTFT) |
| `pairwise` | `decidePairwiseSpeedup` | return `pairwise_insufficient_data` (delayed safety-net secondary) |
| `legacy` | `decideLegacySecondaryFaster` | — |

Regardless of policy, `IsUnresponsiveParticipant(primary)` runs first and
forces an immediate parallel attempt.

Why no cascade for `host_score`? If legacy TTFT could override host-score's
"no", it would launch wasteful secondaries to candidates we already know
are slower — and worse, those races would feed back into the formula as
"losses" for the unfairly-selected host, suppressing its Elo even more
(self-reinforcing exile). Trust the formula or pick a different policy.

To roll back to pre-host-score behaviour, set
`redundancy.speed_policy = "hybrid"` at runtime — no restart needed.

## The score formula

For a (model, host, bucket) key, `HostScoreTracker.ScoreHost` returns a
**lower-is-better** score on a millisecond scale. Two layers:

| Layer | Activates when | Returns |
|---|---|---|
| **1 · direct H2H win-rate** | `PairwiseTracker.H2HWinRate(host, opponent)` has ≥ `MinSamples` direct comparisons | `1 − rate` (so a 70%-win host scores 0.30) |
| **2 · Elo + timing + UCB** | otherwise (when this host has ≥ `MinSamples` own samples) | `base − α·(Elo − 1500) − c·√(ln N_bucket / n_host)` |

Layer 1 is preferred whenever it has data because it measures *exactly the
matchup we're asking about* with zero confounders. Layer 2 is the fallback
for cases where we have separate per-host samples but no direct head-to-head.

## Layer 1: direct H2H win-rate

When primary `A` and candidate `B` have been raced against each other at
least `HostScoreMinSamples` (= 3) times in this bucket,
`PairwiseTracker.H2HWinRate(model, bucket, B, A)` returns B's fraction of
total-time wins over A in [0, 1].

We translate to score so that lower = better:

```
score(B) = 1 − rate(B beats A)
```

A candidate that beats the primary 70% of the time has `score = 0.30` and
the primary has effectively `0.50` (the implicit indifference value). The
margin check in `decideHostScoreSpeedup` is "rate ≥ 0.5 + HostScoreH2HMargin"
(default 0.60 → only act when B wins ≥ 60% of head-to-heads).

## Layer 2: Elo + timing + UCB

When direct H2H data is absent, we fall back to a synthesis of the host's
own recent samples plus a global Elo rating. The final score is:

```
score = base − Elo_credit − UCB_bonus
```

Each term contributes on the same ms scale; sections below explain how
each component is computed.

### Per-host Total time

`HostInvolvement.TotalTimeMs` is computed as `lastChunkAt − sendTime` —
the real moment the host emitted its last chunk, per-host. This matters
in speculative routing: a naive `time.Since(sendTime)` measured at
request-end gives every racing host roughly the same value (the gateway
lifecycle duration), so Total comparisons between hosts in the same race
collapse to noise. With the per-host measurement, a host that streamed
100 chunks in 5 s reports `5000ms` while a sibling that streamed 30
chunks in 1.5 s reports `1500ms`, even though both share the same
overall request envelope.

Fallback: when a host emitted no chunks (`lastChunkAt == 0` — empty
response, cold timeout), TotalTimeMs falls back to `time.Since(sendTime)`
since per-chunk data is unavailable. Such samples are anyway gated out
of Elo by the `Finished` flag in most cases.

### Layer 2.1 · base timing

`base = (1 − γ)·TTFT_p50 + γ·Total_p50`

- `γ = HostScoreNonStreamGamma = 1.0` for non-streaming requests (only
  total time matters — client doesn't see partial output anyway)
- `γ = HostScoreStreamGamma = 0.3` for streaming requests (we still care
  about end-to-end but TTFT meaningfully shapes UX)

Percentiles are over the last `HostScoreWindowSize = 50` samples.

### Layer 2.2 · Elo credit

`Elo_credit = α · (Elo − 1500)`

`Elo` is the chess-style rating of this host in this (model, bucket). It
updates after every observed 2-host pair in `RecordRequest`. A host that
consistently wins drifts above 1500; a consistent loser drifts below.

**Dual-metric K-split.** Per pair we run TWO Elo updates — one keyed on
`FirstTokenMs`, one on `TotalTimeMs` — each with `K/2 = 8`. This mirrors
the selection-score formula (which combines TTFT and Total via `γ`) so the
rating reflects both dimensions:

- both metrics agree (host fast on both) → ≈ K/2 net gain (near full budget,
  minus small Elo self-correction)
- metrics disagree (fast on one, slow on the other) → net ~0
- host fast on one, no signal on the other → ~K/4 gain
- both metrics fail → host loses on both updates (≈ K)

**Per-update outcome rule** (applied independently to TTFT and Total):

| A status | B status | outcome | rationale |
|---|---|---|---|
| both produced metric | both produced metric | faster metric wins (`Sa=1, Sb=0`) | normal race |
| produced metric | failed | A wins (`Sa=1, Sb=0`) | responder beats no-show |
| failed | produced metric | B wins (`Sa=0, Sb=1`) | symmetric |
| both failed | both failed | both lose (`Sa=Sb=0`) | symmetric penalty — each loses K·E_self toward default |

"Produced metric" means:
- for TTFT update: `Responsive && FirstTokenMs > 0`
- for Total update: `Responsive && Finished && TotalTimeMs > 0`

A canceled-but-responded loser (Finished=false, FirstTokenMs>0) still loses
on the TTFT dimension and loses on Total (no completion). A completely
unresponsive host loses on both — exactly what we want, since under the old
single-metric rule unresponsive hosts were filtered out of Elo entirely and
accumulated false high ratings.

**Bradley-Terry continuous outcome.** When both hosts produce the metric,
the pair outcome is a smooth function of their ratio rather than a binary
win/loss:

```
Sa = mb^k / (ma^k + mb^k)        // k = HostScoreBradleyTerryExp = 2
```

For LOWER-is-better metrics: ratio 1.0 → Sa=0.5 (exact draw, no Elo move);
ratio 1.1 → Sa≈0.55 (tiny move); ratio 2.0 → Sa=0.80 (clear win); ratio 10
→ Sa≈0.99 (decisive). This is the standard Bradley-Terry pair-comparison
model (Zermelo 1929, Bradley & Terry 1952) — same statistical pedigree as
Elo itself.

Why this matters: under speculative routing many races are decided at the
streaming-throughput ceiling, where 4 hosts finish within 0.3% of each
other on a 4-minute generation. A naive binary outcome rewards Elo for
those sub-1% margins (pure measurement noise). Bradley-Terry collapses
near-tie ratios to Sa≈0.5 automatically — no explicit noise threshold
needed. Empirically verified on production data: under the old binary
rule a host with 78s p50 TTFT was ranked top-1 in 4 of 6 buckets because
it kept "winning" Total comparisons by milliseconds on slow races; under
BT it correctly sinks to the bottom in every bucket.

`α = HostScoreEloAlpha = 10 ms per Elo point` is the dimensional conversion
that lets us subtract Elo from the ms-scale base. 200-point gap → 2000 ms
credit. That's the size of one extra full-token chunk and is comparable to
the empirical noise floor of total-time differences we measured in
production ([2026-05-24-stats.md](../../docs/chat-api/2026-05-24-stats.md)).

Translating Elo numbers to win probability (canonical chess formula
`P(A) = 1 / (1 + 10^((Rb−Ra)/400))`):

| Elo gap | win-rate of higher-rated host |
|---:|---:|
| +100 | ≈ 64% |
| +200 | ≈ 76% |
| +300 | ≈ 85% |
| +400 | ≈ 91% |

A 200-point gap is the practical "act on it" threshold — smaller gaps
(under 100) are statistically indistinguishable from noise; the speedup
margin check (next section) and the H2H gate filter them out.

Convergence is fast in practice. With `K=16` and a true 70/30 split,
simulated ratings settle within ±100 points of the steady-state gap
(`400·log10(0.7/0.3) ≈ 147`) inside ~50 games, and absorb most noise by
150 games. Raise `K` for faster reaction (more noise); lower for slower
adaptation (more stable).

### Layer 2.3 · UCB exploration bonus

`UCB_bonus = c · √(ln N_bucket / n_host)` (subtracted from score)

This is the [UCB1](https://en.wikipedia.org/wiki/Multi-armed_bandit#Upper_confidence_bounds)
exploration term applied to the Layer-2 score. Without it, a host that
landed in our sample set with a *single* slow run could be exiled from
secondary selection forever — Elo never updates if it never races. The
UCB term grants an "exploration credit" to under-sampled hosts so they get
chosen against established competitors and either prove themselves (their
Elo recovers) or confirm the negative (Elo settles low).

`c = HostScoreUCBCoefficient = 300 ms`. Set to 0 to disable exploration
entirely. The credit shrinks logarithmically as samples accumulate.

In numbers, for `N_bucket = 200` total samples in a bucket:

| host samples `n` | UCB credit (ms) |
|---:|---:|
| 3 | ≈ 921 |
| 10 | ≈ 504 |
| 50 | ≈ 226 |
| 100 | ≈ 160 |
| 200 | 0 (saturated) |

The `n < MinSamples` zone is filtered out earlier in `ScoreHost` (no score
returned), so UCB never produces a degenerate value at the very low end.

### Architecture: host_score / picker separation of concerns

The session picker is responsible solely for **nonce-bound dispatch**: it
matches each newly available nonce to a queued request whose
`excludeParticipants` set permits the binding host. It does not know
about Elo, host scoring, or routing preference.

host_score lives **above** the picker. `decideHostScoreSpeedup` decides
only **how many** speculative secondaries to launch — based on whether
any candidate beats primary on H2H win-rate or Elo+timing score. Which
host receives each secondary is determined entirely by nonce arrival
order, exactly the same path used by primary attempts and every other
escalation/retry path.

This separation matters: the picker's branch logic is hot-path,
historically stable, and serializes nonce dispatch across all attempts.
Pulling host_score knowledge into the picker would couple two systems
that evolve at very different rates. If a future requirement needs to
bias dispatch toward specific hosts, the right shape is to express it
as *additional* exclude entries on the secondary's pickerRequest — the
picker's existing exclude/ghost-burn machinery already enforces "no
dispatch on these hosts" semantics without modification.

### Putting it together

Five representative scenarios, all with `base = 5000 ms`, `N_bucket = 100`,
in non-streaming mode (`γ = 1.0`):

| scenario | Elo | n | − Elo credit | − UCB bonus | final score |
|---|---:|---:|---:|---:|---:|
| established fast | 1620 | 50 | −1200 | ≈ −91 | **3709** |
| established slow | 1380 | 50 | +1200 | ≈ −91 | **6109** |
| cold newcomer | 1500 | 3 | 0 | ≈ −372 | **4628** |
| cold underdog | 1400 | 3 | +1000 | ≈ −372 | **5628** |

(`−` / `+` reflect whether the adjustment helps or hurts the score.)

Reading the table:

- **established fast** wins outright — Elo credit dominates.
- **cold newcomer** beats **established slow** purely thanks to UCB,
  which is the entire point of adding exploration.
- **cold underdog** is helped by UCB but not enough to overcome its
  negative Elo — converges toward demotion as samples accumulate.

The takeaway: **cold hosts get a fair chance** thanks to the UCB term and
**proven-fast hosts dominate** thanks to Elo credit, both on the same ms
scale so they can be compared.

## Hyperparameters

All declared in [`host_scores.go`](../cmd/devshardctl/host_scores.go) at
the top of the file. Mutable at runtime (no atomic — change at quiet
times). Defaults chosen to match the empirical scale we measured:

| Var | Default | What it controls |
|---|---:|---|
| `HostScoreWindowSize` | 50 | sliding ring per (model, host, bucket) — older samples roll off |
| `HostScoreMinSamples` | 3 | both layers gate on this; below it ScoreHost returns `(0, false)` |
| `HostScoreEloK` | 16 | classic chess K-factor; bigger = faster convergence + more noise. Global only — not per-bucket. |
| `HostScoreEloDefault` | 1500 | starting rating for unknown hosts |
| `HostScoreEloAlpha` | 10 | ms credit per Elo-point deviation from 1500 |
| `HostScoreStreamGamma` | 0.3 | TTFT vs Total weight for streaming requests |
| `HostScoreNonStreamGamma` | 1.0 | … for non-streaming requests |
| `HostScoreUCBCoefficient` | 300 | exploration bonus scale, ms; 0 disables |
| `HostScoreH2HMargin` | 0.10 | Layer 1 trigger: candidate must win ≥ 50% + this |
| `HostScoreSpeedupMargin` | 0.10 | Layer 2 trigger: candidate score must be ≤ primary · (1 − this) |
| `HostScoreEloHalfLife` | 12 h | half-life for stale-Elo decay; 0 disables (see Regime change). Override per-bucket via `HostScoreBucketOverrides` (see [Per-bucket calibration](#per-bucket-calibration)) |
| `HostScoreExplorationEpsilon` | 0.02 | ε-greedy probability of launching a random secondary when the score-based path is ambivalent |
| `RedundancySpeedPolicy` | `host_score` | which decision maker `Decide()` selects (see When this is consulted) |

## Per-bucket calibration

`HostScoreEloHalfLife` benefits from per-bucket tuning: high-traffic
buckets receive many fresh samples per hour and can afford to forget
older ratings quickly, while sparse buckets need a longer half-life to
keep any signal at all.

The mechanism is one map declared at the top of [`host_scores.go`](../cmd/devshardctl/host_scores.go):

```go
HostScoreBucketOverrides = map[string]HostScoreBucketOverride{
    "lt_1k": {HalfLife: 6 * time.Hour},
    "1k_5k": {HalfLife: 6 * time.Hour},
    // Other buckets inherit HostScoreEloHalfLife (12h).
}
```

`HostScoreBucketOverride` has one field, `HalfLife`. Negative inherits
the global `HostScoreEloHalfLife`; zero explicitly disables decay for
that bucket. Read path is `hostScoreHalfLifeForBucket(bucket)`.

### Why these values

Rating-system literature (FIDE Handbook §B.02.10.6, Glickman 1995 on
adaptive ratings) consistently recommends faster decay where ratings
refresh often. The two committed overrides target precisely the
high-traffic buckets where 12 h would be unnecessarily slow:

| Bucket | HalfLife | Rationale |
|---|---:|---|
| `lt_1k` | 6 h | high traffic (hundreds/host/hour) — afford faster adaptation |
| `1k_5k` | 6 h | high traffic — same |
| `5k_15k` and above | 12 h (inherit) | replay-loop data did not justify compressing further |

K-factor is not per-bucket — `HostScoreEloK = 16` is global. If
calibrating K per bucket ever becomes worthwhile, the `HostScoreBucketOverride`
struct can be extended; the corresponding helper would follow the same
pattern as `hostScoreHalfLifeForBucket`.

## Decision: when host scoring launches a secondary

[`Redundancy.decideHostScoreSpeedup`](../cmd/devshardctl/redundancy.go)
always returns a complete `Decision`. The `Reason` field tells operators
exactly what happened. Per candidate:

```
hadData = false
for candidate in candidates:
    if H2H(candidate vs primary) has ≥ MinSamples samples:
        hadData = true
        if win_rate ≥ 0.5 + HostScoreH2HMargin:
            accepted += 1
        continue   # H2H is authoritative once it has data

    if score(candidate) is defined:                  # ≥ MinSamples own samples
        hadData = true
        if score(candidate) < score(primary) · (1 − HostScoreSpeedupMargin):
            accepted += 1

if accepted > 0:
    return Decision{RunSecondary: true, Reason: "host_scores", attempts: accepted}

if rand() < HostScoreExplorationEpsilon:
    return Decision{RunSecondary: true, Reason: "host_scores_exploration", attempts: 1}

if hadData:
    return Decision{Reason: "host_scores_no_candidate"}  # decisive: nobody beats primary

return Decision{Reason: "host_scores_no_data"}           # cold start, group_size≤1, quarantine
```

Cap on `accepted` is `PairwiseMaxProactiveAttempts` (3). Possible `reason`
values for `host_score` policy:

| `reason` | `RunSecondary` | Meaning |
|---|---|---|
| `host_scores` | true | confident score-based pick |
| `host_scores_exploration` | true | ε-greedy fired (random candidate; the cold-start bootstrap mechanism) |
| `host_scores_no_candidate` | false | had data, nobody beat primary by margin |
| `host_scores_no_data` | false | cold start, all quarantined, `group_size ≤ 1`, etc. |

## Regime change & recovery

The formula above is built for *steady-state* speed differences. Two
mechanisms protect it from the failure mode where a host changes regime
(was slow, became fast — or vice versa) and the algorithm refuses to notice.

### Elo time-decay

Without intervention, an Elo rating set 10 days ago is treated identically
to one set 10 minutes ago — there is no time-component in the K-update
formula. A host that drifted to 1200 over a week and then quietly improved
would need dozens of `decideHostScoreSpeedup`-triggered races just to climb
back to neutral, but the same algorithm refuses to *route* to it because
its stored Elo is still 1200. Self-perpetuating exile.

We pull stored ratings toward `HostScoreEloDefault` on read via a
half-life decay:

```
decayed(R, age) = 1500 + (R − 1500) · 2^(−age / HostScoreEloHalfLife)
```

With `HostScoreEloHalfLife = 12h` (table uses the global default for
illustration; **per-bucket overrides apply in practice — see
[Per-bucket calibration](#per-bucket-calibration)**):

| Age since last update | Gap multiplier | 1800 → | 1200 → |
|---|---:|---:|---:|
| 0 (fresh) | 1.000 | 1800 | 1200 |
| 6 h | 0.707 | 1712 | 1288 |
| 12 h (one half-life) | 0.500 | 1650 | 1350 |
| 24 h | 0.250 | 1575 | 1425 |
| 48 h | 0.063 | 1519 | 1481 |
| 96 h | 0.004 | 1501 | 1499 |

For a bucket with a shorter half-life the same multipliers fire at
proportionally earlier ages: with `lt_1k`'s 3 h, 6 h elapsed already
halves the gap; with `gte_100k`'s 12 h it matches the table exactly.

This applies on every read (`ScoreHost`, `Snapshot`) *and* before every
write in `updateEloLocked` — so the K-update is applied to the
*time-adjusted* rating, not the stale one. Persisted state carries
`UpdatedAt` per row so decay survives restart.

Set `HostScoreEloHalfLife = 0` (or the bucket's override to `0`) to
disable decay entirely.

### ε-greedy exploration floor

UCB ([Layer 2.3](#layer-23--ucb-exploration-bonus)) shrinks logarithmically:
once a host has 50 samples its bonus is tiny (≈ 200 ms in a normal bucket),
which is not enough to dislodge an Elo favourite with a 5+-second
advantage. So purely UCB-based exploration doesn't help once a host is
"established mediocre."

The ε-greedy floor adds a small unconditional exploration budget. When
`decideHostScoreSpeedup` would otherwise return *no* secondary (neither
H2H nor score-based justifies it), it rolls one random number: with
probability `HostScoreExplorationEpsilon` it launches *one* candidate
anyway, tagged `reason="host_scores_exploration"`.

| Setting | Behavior |
|---|---|
| ε = 0 | exploration disabled; pure score-based routing |
| ε = 0.02 (default) | ≈ 1 in 50 ambivalent decisions becomes a forced race; Elo evolves |
| ε = 1.0 | always race when score is ambivalent (useful for tests) |

The cost is one extra inference per 50 ambivalent decisions. The benefit
is that recently-improved hosts get races, win them, and climb out of low
Elo within hours instead of days.

### Putting both together

A host that was slow for 10 days, then becomes fastest. Timing below
uses the **global 12 h** half-life; for `lt_1k`/`1k_5k` (6 h override)
the same milestones land at roughly half the elapsed time.

1. **Hour 0** of regime change: Elo ≈ 1200, ring full of slow samples.
   Decay hasn't kicked in (last race was minutes ago).
2. **Hours 0–12**: ε-greedy occasionally routes to it. Each win is a
   real K-update on its decayed (still 1200-ish) Elo: `K · (1 − E)`
   where `E ≈ 0.15` → ≈ +13.6 per win. Slow but steady climb.
3. **Hours 12+ without traffic** (between rare ε-greedy races): decay
   half-life kicks in. After 24 hours of low traffic, the 300-point
   gap has halved to 150. Now ε-greedy wins yield larger Elo updates
   (`E ≈ 0.30` → +11.2 per win, but starting from 1350 instead of 1200).
4. **Within 1–3 days**: enough wins accumulated for the ring to
   refresh too, and the host promotes back to score-based routing
   without ε-greedy help.

Decay alone protects against complete exile; ε-greedy alone keeps races
flowing; both together compress recovery from days to hours.

## Observability

`GET /v1/admin/host_scores` (admin-auth-gated; optional `?model=` and
`?bucket=` filters):

```json
{
  "snapshots": [
    {
      "model": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
      "host":  "gonka1...",
      "bucket": "1k_5k",
      "samples": 26,
      "bucket_total_samples": 56,
      "ttft_p50_ms": 843, "ttft_p90_ms": 1240,
      "total_p50_ms": 8203, "total_p90_ms": 14500,
      "elo": 1628.4,
      "ucb_bonus_ms": 117.6,
      "score_stream": 5347.2,
      "score_non_stream": 7393.8
    }, ...
  ],
  "generated_at": "2026-05-24T...",
  "settings": { ... all tunables ... }
}
```

What to look at on a live node:

- **Elo spread within a bucket.** Two hosts with 200+ point separation
  after ≥ 20 samples each is a strong signal; under 100 is noise.
- **`bucket_total_samples` and per-host `samples`.** If one host is
  dominating sample volume (>80% of bucket total), the picker is
  effectively single-host on this shape — exploration is doing nothing
  because there's no alternative.
- **`ucb_bonus_ms` distribution.** Should be near-zero for established
  hosts and 200-800 ms for hosts with < 10 samples.
- **`score_stream` vs `score_non_stream`.** Diverge meaningfully only
  when TTFT and Total differ greatly. If they're identical → γ is doing
  nothing on this workload (legitimate; just means TTFT and Total are
  proportional).
## Performance quarantine

Layers 2.1–2.3 demote slow hosts in the *secondary-selection* path: a
candidate with bad scoring never wins the speedup race. They do nothing
to stop a slow host from being assigned the **primary** attempt — the
session picker is nonce-driven and policy-agnostic, so a host with
p50 TTFT = 70 s still takes its turn at primary slots. To stop
persistently slow hosts from soaking primary nonces, the host_score
policy adds a fifth quarantine channel alongside the existing four
(HTTP 429/503, transport failure, empty stream, stalled winner).

### Algorithm

For every observed responsive TTFT sample, the limiter performs:

1. **Update per-host ring.** Each (host, bucket) keeps a rolling window
   of the last `hostScoreSampleRingCapacity` (= 20) TTFTs. The new
   sample is appended.

2. **Compute bucket threshold (excluding self).** The threshold is the
   p90 of all TTFTs from *other* qualifying hosts in the bucket — every
   host except the one being evaluated, restricted to those with at
   least `HostScoreQuarantineMinSamplesPerHost` (= 3) samples in their
   ring. The bucket must have at least
   `HostScoreQuarantineMinQualifyingHosts` (= 3) such hosts. Otherwise
   no threshold is defined and the sample is ignored. Excluding the
   candidate's own samples prevents a slow host's bad samples from
   raising the threshold against which the same host is judged
   (self-pollution).

3. **Sliding-window strike.** Each (host, bucket) keeps a ring of the
   last `hostScoreSlidingWindow` (= 5) outcomes (bad/good). The new
   sample is recorded as `bad` iff `TTFT > threshold`. When the
   window holds at least `hostScoreBadInWindow` (= 3) bad outcomes,
   the host enters quarantine *in this bucket*. The window is cleared
   on entry so a fresh accumulation can start after release.

4. **Exponential backoff.** Each bucket also remembers its recent
   quarantine entry timestamps within the last
   `hostScoreQuarantineHistoryWindow` (= 4 h). The duration of the new
   quarantine is `hostScoreQuarantineBaseDuration · 2^(recent_count)`,
   capped at `hostScoreQuarantineMaxDuration`. With the defaults: 1st
   strike → 30 min, 2nd within 4 h → 60 min, 3rd → 120 min (cap).
   After 4 h with no entry, the count resets to zero.

5. **Picker integration.** `ParticipantRequestLimiter.IsRecentlyQuarantined`
   returns true if **any** bucket holds an active Layer 3 quarantine
   for the host. The picker treats this identically to the four other
   quarantine channels — host is removed from both primary picker
   rotation and secondary candidate scans.

### Why sliding window, not consecutive strikes

Bimodal hosts ("90% fast, 10% catastrophic") emit pattern
`slow, slow, fast, slow, ...` — a consecutive-strike counter resets on
every fast sample and never reaches 3-in-a-row despite 60%+ bad rate.
The sliding "≥3 of last 5" counter captures these without false
positives on hosts with single transient slow bursts.

### Why exclude self from threshold

When a slow host adds enough slow samples to the bucket pool, those
samples shift the pool's own p90 toward "slow". The bad host stops
crossing its own threshold and escapes detection. Excluding the
evaluated host's own samples from the threshold pool makes the test
"this host vs. the rest of the cohort" rather than "this host vs.
a cohort that includes itself".

### Policy gate

The whole channel is **gated on `RedundancySpeedPolicy ==
"host_score"`**. Under any other policy the limiter's
`RecordHostScoreSample` and `isQuarantined` early-return without
acquiring locks or mutating state. The gate is checked on every call,
so flipping the runtime policy instantly disarms the channel without
needing a separate feature flag.

### Validation (replay-loop, 1,580 minutes, 12 hosts, 6 buckets)

| Metric | Value |
|---|---:|
| Truly bad responsive (host, bucket) pairs | 23 |
| True positives | 20 |
| False positives | 7 (all borderline 7–20 % bad rate) |
| Precision | 74 % |
| Recall | 87 % |
| Bimodal hosts caught (vs consecutive strike variant) | yes |

All FPs sit between 7 % and 20 % bad rate — i.e. just below the 20 %
threshold used to define "truly bad" in the ground truth. Tightening
the analytical threshold to 15 % moves every FP into TP and yields
near-100 % precision.

### Hyperparameters

| Var | Default | Meaning |
|---|---:|---|
| `HostScoreQuarantineMinSamplesPerHost` | 3 | each host needs ≥ this many TTFTs to enter the threshold pool |
| `HostScoreQuarantineMinQualifyingHosts` | 3 | bucket needs ≥ this many qualifying hosts before any threshold is defined |
| `hostScoreSampleRingCapacity` | 20 | per-(host, bucket) recent-TTFT ring |
| `hostScoreSlidingWindow` | 5 | bad/good outcome window per (host, bucket) |
| `hostScoreBadInWindow` | 3 | ≥ this many bad outcomes in window → quarantine |
| `hostScoreQuarantineBaseDuration` | 30 min | first quarantine duration |
| `hostScoreQuarantineMaxDuration` | 120 min | cap for exponential backoff |
| `hostScoreQuarantineHistoryWindow` | 4 h | sliding window over which recent quarantines accumulate backoff |

### Known limitations (v1)

- **No persistence.** Layer 3 state lives only in memory; a gateway
  restart releases every active quarantine. The other quarantine
  channels persist via `ParticipantThrottleStore`. Adding persistence
  for Layer 3 is a planned follow-up.
- **No metrics counter.** `log.Printf` lines tag entries (`host_score_quarantine_entered`)
  and expirations (`host_score_quarantine_expired`) but there is no
  Prometheus counter yet. Operators can grep logs for now.
- **isQuarantined is host-level.** Internal state is per (host,
  bucket), but the public check `IsRecentlyQuarantined(host)` returns
  true if *any* bucket is active. The picker has no bucket context at
  nonce-binding time, so a host bad in `lt_1k` is removed from
  primary rotation for `gte_100k` traffic too. Plumbing bucket through
  the picker is a separate, larger refactor.

## Persistence and restart

`HostScoreTracker.PersistState` and `Restore` round-trip the full state
through SQLite table `host_score_state` (schema in
[`perfstore.go`](../cmd/devshardctl/perfstore.go)). Each row stores the
full sample ring as a JSON blob plus the Elo rating and an `updated_at_utc`
timestamp marking when that rating was last K-updated.

`updated_at_utc` is what makes time-decay survive restart: on `Restore`
we seed `eloUpdatedAt[key]` from this column, so the decay clock keeps
ticking across shutdowns rather than resetting to "now". A node that
sleeps long enough to clear one or more bucket-specific half-lives boots
up with already-half-decayed ratings on first read.

Flush schedule:
- Periodic every `hostScoreFlushInterval = 2 min` (background goroutine
  `hostScoreFlushLoop`)
- Once on `Gateway.Close()` for clean shutdown

Restore happens once in `PerfTracker.loadFromStore` at startup before any
new sample is recorded.

## Related

- [host-health.md](host-health.md) — quarantine and perf tracking
- [proxy-architecture.md](proxy-architecture.md) — where this fits in
- [`2026-05-24-stats.md`](../../docs/chat-api/2026-05-24-stats.md) —
  production capture analysis that motivated this work
- [`2026-05-24-formulas.md`](../../docs/chat-api/2026-05-24-formulas.md)
  — formula search and selection
- [`2026-05-24-formulas-impl.md`](../../docs/chat-api/2026-05-24-formulas-impl.md)
  — Phase 1 / Phase 2 implementation plan

