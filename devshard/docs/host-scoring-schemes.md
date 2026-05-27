# host_score — visual scheme

All scenarios covered as sequence diagrams. Values drawn from the merged replay loop; defaults from `host_scores.go` and `host_score_quarantine.go`.

---

## 0. System overview

Static map of where each layer sits in the request lifecycle. Every later scenario is a specific walk through this graph.

```mermaid
flowchart TD
    A([inference arrives]) --> B[bucket = lt_1k / 1k_5k / ... / gte_100k]
    B --> C[Picker — next free nonce binds to a host]
    C --> D{IsRecentlyQuarantined&#40;host&#41;<br/>any of 5 channels?}
    D -- yes --> Z[skip nonce, try next]
    D -- no --> E[Primary assigned]
    E --> F[decideHostScoreSpeedup]
    F --> G{branches}
    G -- unresponsive primary --> P1[primary_unresponsive<br/>force secondary]
    G -- H2H rate ≥ 60% --> P2[host_scores<br/>accept H2H winner]
    G -- Layer 2 score beats margin --> P3[host_scores<br/>accept candidate]
    G -- no data --> P4[no_data<br/>primary alone]
    G -- data, no candidate --> P5[no_candidate<br/>maybe ε-greedy 2%]
    P1 --> R[race]
    P2 --> R
    P3 --> R
    P5 --> R
    P4 --> R
    R --> S[record per-host sample]
    S --> L3[Layer 3: bucket pool + 3-of-5 window]
    L3 --> L3Q{3-of-5 bad?}
    L3Q -- yes --> L3F[quarantine 30 / 60 / 120 min]
    L3Q -- no --> N([end])
    L3F --> N
```

---

## 1. Scenario — fast primary, no secondary needed

The picker binds the request to a top-Elo host. host_score sees that no candidate beats the primary by the speedup margin, so no secondary is launched and the primary runs alone. Speculative cost is zero.

```mermaid
sequenceDiagram
    actor User
    participant Picker
    participant host_score
    participant Layer3
    participant fast_primary as 9stckwnz (Elo=1682)

    User->>Picker: request (256 tok, bucket=lt_1k)
    Picker->>Layer3: IsRecentlyQuarantined(9stckwnz)?
    Layer3-->>Picker: false
    Picker->>host_score: Decide(primary=9stckwnz)

    Note over host_score: primary.score = -1285ms (base 535 minus Elo credit 1820). No candidate beats primary × 0.9.
    host_score-->>Picker: no_candidate, RunSecondary=false

    Picker->>fast_primary: send
    fast_primary-->>User: TTFT 535ms, Total 597ms (winner)
    fast_primary->>Layer3: record_sample(535ms)
    Note over Layer3: window slides: [good]. No strike.
```

---

## 2. Scenario — slow primary, secondary wins

A slow host got the primary nonce. host_score finds a much faster candidate and spawns it. The two race in parallel, the secondary returns first, the slow primary is cancelled. User sees roughly 4× speedup at the cost of one extra nonce.

```mermaid
sequenceDiagram
    actor User
    participant Picker
    participant host_score
    participant slow_primary as fglnkns2 (Elo=1420)
    participant fast_secondary as 9stckwnz (Elo=1665)

    User->>Picker: request (6144 tok, bucket=5k_15k)
    Picker->>host_score: Decide(primary=fglnkns2)

    Note over host_score: primary.score = 2860ms. 9stckwnz.score = -744ms. -744 < 2860 × 0.9 = 2574 — accept candidate.
    host_score-->>Picker: host_scores, ImmediateAttempts=1

    par primary path
        Picker->>slow_primary: send
        slow_primary-->>Picker: TTFT 1.5s, still streaming
    and secondary path
        Picker->>fast_secondary: send (excludes fglnkns2)
        fast_secondary-->>User: TTFT 839ms (winner)
    end

    Note over Picker,slow_primary: slow_primary cancelled as the loser.
```

---

## 3. Scenario — Layer 1 H2H trigger overrides Layer 2

When the pairwise tracker holds at least three direct head-to-head samples between primary and a candidate, Layer 1 win-rate is consulted first and overrides the Layer 2 score path entirely. A win rate at or above 60% is accepted without looking at base, Elo, or UCB terms. This makes direct empirical evidence outrank inferred scores.

```mermaid
sequenceDiagram
    actor User
    participant Picker
    participant host_score
    participant PairwiseTracker
    participant primary_host
    participant candidate_host

    User->>Picker: request
    Picker->>host_score: Decide(primary=primary_host)

    Note over host_score: Layer 1 first
    host_score->>PairwiseTracker: H2HWinRate(candidate_host, primary_host, bucket)
    PairwiseTracker-->>host_score: rate=0.72, n=8 direct samples
    Note over host_score: 0.72 ≥ 0.5 + 0.10 — accept. Layer 2 (base/Elo/UCB) is skipped because Layer 1 data is authoritative.

    host_score-->>Picker: host_scores, ImmediateAttempts=1
    par
        Picker->>primary_host: send
    and
        Picker->>candidate_host: send
    end
    candidate_host-->>User: wins (consistent with H2H history)
```

---

## 4. Scenario — UCB exploration (under-sampled host gets a structured bonus)

UCB ("upper confidence bound") is Layer 2's built-in answer to a specific failure mode: a host with few samples can look slightly worse than the primary on raw base/Elo numbers, but we are not confident in that comparison yet. Without correction we'd never race it and never learn whether it's actually faster. UCB adds a bonus to under-sampled hosts that decays as samples accumulate: `UCB = c · √(ln(N_bucket) / n_host)` with `c = HostScoreUCBCoefficient = 300 ms`. The bonus is *subtracted* from the candidate's score (lower score = more attractive), so a host with `n=5` samples gets ≈ 350 ms of help while a host with `n=500` gets only ≈ 35 ms. Unlike ε-greedy (an unconditional 2% random roll), UCB is *targeted* — it directs exploration at exactly the hosts we know least about.

```mermaid
sequenceDiagram
    actor User
    participant Picker
    participant host_score
    participant stable_host
    participant new_host

    User->>Picker: request, bucket=1k_5k
    Picker->>host_score: Decide primary=stable_host
    Note over host_score: stable: n=200, base p50=1200ms, Elo=1500
    Note over host_score: stable UCB = 300·√(ln(1000)/200) ≈ 35ms, score ≈ 1165
    Note over host_score: candidate new_host: n=5, base p50=1280ms (slightly slower than primary), Elo=1500
    Note over host_score: new_host UCB = 300·√(ln(1000)/5) ≈ 353ms, score ≈ 927
    Note over host_score: Margin check: 927 ≤ 1165 · (1 − 0.10) = 1048, accept candidate.
    Note over host_score: Without UCB, new_host would lose on raw base (1280 vs 1200). UCB makes the race happen.

    host_score-->>Picker: host_scores, ImmediateAttempts=1
    par
        Picker->>stable_host: send
    and
        Picker->>new_host: send
    end
    new_host-->>User: TTFT 900ms (wins the race)
        Note over host_score: Outcome feeds Elo (new_host gains rating) and sample count (n=5 to 6).
        Note over host_score: UCB shrinks for the next decision. By n=50 the bonus is ≈ 111ms, by n=500 it is ≈ 35ms.
        Note over host_score: A genuinely mediocre host stops getting raced once UCB decays and Elo dominates.
```

How UCB and ε-greedy divide the work: UCB is *deterministic structured exploration* aimed at uncertain hosts during Layer 2 scoring. ε-greedy (next scenario) is the *unconditional safety net* that fires after Layer 2 has nothing — covering hosts UCB cannot reach (e.g., zero samples means UCB formula is skipped) or scenarios where every candidate's Elo has drifted high enough that UCB alone can't bring them within the 10% margin.

---

## 5. Scenario — ε-greedy exploration (cold-start helper)

All Layer 1 and Layer 2 candidates fail the margin check (or have no data), but the ε-greedy floor still rolls true with probability 2%. A random non-tried host is then given a forced shot. This is how cold or recovering hosts get a chance to climb back into rotation. The exploration cost is bounded by ε itself: 2% of the would-be-no-secondary decisions.

```mermaid
sequenceDiagram
    actor User
    participant Picker
    participant host_score
    participant primary_host
    participant exploration_host

    User->>Picker: request
    Picker->>host_score: Decide(primary=primary_host)

    Note over host_score: Layer 1: no direct H2H data. Layer 2: candidates have data but none beats primary × 0.9.
    Note over host_score: hadData=true, roll random() = 0.012 < 0.02 — fire exploration.

    host_score-->>Picker: host_scores_exploration, ImmediateAttempts=1
    Picker->>exploration_host: send (any non-excluded host)
    par
        Picker->>primary_host: send
    and
        Picker->>exploration_host: send
    end

    Note over Picker: Outcome of the race feeds Elo so the exploration_host's rating drifts over time toward its true level.
```

---

## 6. Scenario — primary unresponsive, immediate parallel

`PerfTracker.IsUnresponsiveParticipant(primary)` is the only check that runs before the Layer 1 and Layer 2 logic. If the primary has a recent history of failing to send any chunks, a backup is launched immediately and unconditionally. This bypass exists because the host is structurally broken right now — no point computing scores.

```mermaid
sequenceDiagram
    actor User
    participant Picker
    participant host_score
    participant PerfTracker
    participant primary_host as primary_host (recently silent)
    participant backup_host

    User->>Picker: request
    Picker->>host_score: Decide(primary=primary_host)

    host_score->>PerfTracker: IsUnresponsiveParticipant(primary_host)?
    PerfTracker-->>host_score: true
    Note over host_score: Short-circuit: skip Layer 1 and Layer 2. RunSecondary=true, ImmediateAttempts=1.

    host_score-->>Picker: primary_unresponsive
    par
        Picker->>primary_host: send (may stall)
    and
        Picker->>backup_host: send
    end
    backup_host-->>User: response (likely first)
```

---

## 7. Scenario — Layer 3 quarantine fires (consecutive slow)

Three slow TTFTs from the same host land in its own (host, bucket) sliding window. The bucket threshold is the p90 of *other* hosts' samples (excludes self — no self-pollution). The third bad outcome trips the gate; quarantine duration is 30 minutes for the first event in the 4-hour history window. Subsequent picker checks via `IsRecentlyQuarantined` return true and the host is removed from primary and secondary rotation.

```mermaid
sequenceDiagram
    participant kwerryrs as kwerryrs (lt_1k)
    participant Layer3

    Note over Layer3: bucket has 5 qualifying hosts (excluding kwerryrs). threshold = p90(other-pool) = 1.6s.

    kwerryrs->>Layer3: TTFT 113s (responsive)
    Note over Layer3: 113s > 1.6s. window=[bad]. count=1.

    kwerryrs->>Layer3: TTFT 264s
    Note over Layer3: window=[bad, bad]. count=2.

    kwerryrs->>Layer3: TTFT 282s
    Note over Layer3: window=[bad, bad, bad]. ≥3 of last 5 — fire. history=[]. duration = 30 × 2^0 = 30 min.
    Layer3-->>kwerryrs: quarantineUntil = now + 30 min

    Note over Layer3: From here, IsRecentlyQuarantined(kwerryrs) returns true for any picker check.
```

---

## 8. Scenario — bimodal host caught by sliding window

A bimodal host alternates fast and slow samples. A consecutive-strike counter would never reach 3 — every fast sample resets it. The sliding window doesn't reset, it just slides. Three bad outcomes anywhere in the last 5 are enough. This is the design rationale for choosing a window over a streak counter.

```mermaid
sequenceDiagram
    participant bimodal_host
    participant Layer3

    Note over Layer3: threshold = 1.6s (bucket cohort p90).

    bimodal_host->>Layer3: TTFT 60s (bad)
    Note over Layer3: window=[bad]

    bimodal_host->>Layer3: TTFT 800ms (good)
    Note over Layer3: window=[bad, good]. A consecutive-strike counter would reset here, but the sliding window keeps the bad entry.

    bimodal_host->>Layer3: TTFT 45s (bad)
    Note over Layer3: window=[bad, good, bad]. count=2 (not 1).

    bimodal_host->>Layer3: TTFT 700ms (good)
    Note over Layer3: window=[bad, good, bad, good].

    bimodal_host->>Layer3: TTFT 90s (bad)
    Note over Layer3: window=[bad, good, bad, good, bad]. count=3 ≥ 3 of last 5 — fire.
    Layer3-->>bimodal_host: quarantineUntil = +30 min

    Note over Layer3: A consecutive-strike model would have missed this host despite a 60% bad rate.
```

---

## 9. Scenario — quarantine expiry plus relapse (exponential backoff)

A repeat-offender host's quarantine duration grows as long as recent history (within a 4-hour sliding window) contains prior entries. The duration doubles on each relapse until it hits a 120-minute cap. After 4 hours without an entry, history prunes and the backoff resets to 30 minutes.

```mermaid
sequenceDiagram
    participant kwerryrs
    participant Layer3

    Note over Layer3: t = 0. Three bad in window — fire. history=[]. duration = 30 × 2^0 = 30 min. history becomes [t=0].
    Layer3-->>kwerryrs: quarantineUntil = +30 min

    Note over Layer3: t = 30 min. Expiry path: window cleared, history kept.

    kwerryrs->>Layer3: three more slow samples
    Note over Layer3: t = 31 min. history.prune(cutoff = t-4h) = [t=0]. duration = 30 × 2^1 = 60 min. history becomes [t=0, t=31m].
    Layer3-->>kwerryrs: quarantineUntil = +60 min

    Note over Layer3: t = 91 min. Expiry.

    kwerryrs->>Layer3: three more slow samples
    Note over Layer3: t = 92 min. history.prune = [t=0, t=31m]. duration = 30 × 2^2 = 120 min. Cap applied (max 120). history becomes [t=0, t=31m, t=92m].
    Layer3-->>kwerryrs: quarantineUntil = +120 min
```

---

## 10. Scenario — host recovery (regime change)

A host that was slow long enough to sink its Elo and trigger Layer 3 quarantines actually fixes itself (network repaired, new hardware, catch-up cycle). Layer 3 quarantine expiry returns it to rotation; Elo's half-life decay pulls its rating back toward the 1500 default; ε-greedy occasionally spawns it as a secondary; good outcomes update its rating upward. There is no persistent blocklist — the algorithm self-heals.

```mermaid
sequenceDiagram
    participant recovered_host
    participant Picker
    participant Layer3
    participant host_score as host_score (Elo)

    Note over recovered_host,host_score: Initial state: Elo=1200 (sunk from prior slow streak). Most recent quarantine expired.

    Picker->>host_score: Decide(primary=other_host, candidates include recovered_host)
    Note over host_score: recovered_host did not beat the margin. But ε-greedy random() < 0.02 — exploration fires.
    host_score-->>Picker: host_scores_exploration, ImmediateAttempts=1

    Picker->>recovered_host: send
    recovered_host-->>Picker: TTFT 400ms (winner of race)

    Note over host_score: Elo update on the pair: recovered_host won, +K × (1 - E). Elo: 1200 → 1215.

    Note over host_score: In parallel, half-life decay (6h for lt_1k, 12h global) pulls the rating toward 1500 over time.

    Note over recovered_host: Over hours, repeated wins plus decay restore recovered_host to mid-pack, then to top of the cohort.
```

---

## 11. Exponential backoff — formula, table, and lifecycle

`bucketQuarState.nextQuarantineDuration` in `host_score_quarantine.go` computes the duration on every quarantine entry:

```text
1. prune history: keep only entries within last 4h sliding window
2. duration = HostScoreQuarantineBaseDuration << len(history)   // bit-shift = 30min × 2^count
3. if duration > HostScoreQuarantineMaxDuration: duration = HostScoreQuarantineMaxDuration
4. append now to history
```

Tunables (constants in `host_score_quarantine.go`):

| Constant | Value | Role |
|---|---:|---|
| `hostScoreQuarantineBaseDuration` | 30 min | first quarantine in a fresh history |
| `hostScoreQuarantineMaxDuration` | 120 min | hard cap on growth |
| `hostScoreQuarantineHistoryWindow` | 4 h | sliding window for relapse tracking |

Resulting schedule:

| Recent quarantines for (host, bucket) within 4h | Duration | How long until reset |
|---:|---:|---:|
| 0 | **30 min** | — |
| 1 | **60 min** | 4h from previous |
| 2 | **120 min** (cap) | 4h from previous |
| 3+ | 120 min | 4h from previous |
| (any) idle for 4h | back to **30 min** | history fully pruned |

Lifecycle on a typical repeat offender (already drawn in scenario 8 above):

```mermaid
flowchart LR
    Q1[1st event<br/>30 min lockout] -->|relapse within 4h| Q2[2nd event<br/>60 min lockout]
    Q2 -->|relapse within 4h| Q3[3rd event<br/>120 min CAP]
    Q3 -->|relapse| Q3
    Q3 -.->|4h idle| Q1
    Q2 -.->|4h idle| Q1
```

The history is *only* pruned during the `nextQuarantineDuration` call (and during `clearAll` — the operator-initiated reset, see `participantLimiter.ClearQuarantine`). Background ticking is not required. Confirmed by `TestHostScoreQuarantine_ExponentialBackoff` and `TestHostScoreQuarantine_BackoffResetsAfterHistoryWindow` in `participant_limiter_hostscore_test.go`.
