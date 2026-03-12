# Inference Quality Protocol — Semantic Inference Optimization

> Design document for GiP [#860].
> Infrastructure foundation: PR [#859] (semantic cache + CacheQualityWeight).
> Merge sequence: #793 → #703 → #859 → this roadmap.

---

## Overview

The Gonka network has a rigorous model for **compute quality**: Proof-of-Compute
measures nonce generation and converts it to epoch weight. Every node optimizes for PoC.

**Inference quality** — whether a response was useful, accurate, timely, or appropriate
for the request type — has no equivalent protocol representation. This document proposes
a framework to measure it, route by it, and surface it to developers.

The implementation is phased. Each phase is independently deployable and governance-gated.
No phase breaks existing protocol behavior.

---

## Quality Axis Registry (L0–L9)

Ten axes. Each independently activatable via `axis_weights` governance parameter.
Composite score: `QualityScore = Σ(wi × Li)` where Σwi = 1.0.

| Axis | Name | Source | Measured/Projected | Score (baseline) |
|---|---|---|---|---|
| L0 | Compute stability | Chain (PoC CV) | MEASURED | 0.65 |
| L1 | Availability | Chain (participant churn) | MEASURED | 0.72 |
| L2 | Correctness | Chain (validated/missed/invalidated) | MEASURED | 0.9973 |
| L3 | Relevance | DAPI embed(prompt)↔embed(response) cosine | PROJECTED | 0.914 |
| L4 | Usefulness | `X-Inference-Feedback` header | PROJECTED | 0.854 |
| L5 | Outcome | Developer webhook | PROJECTED | 0.726 |
| L6 | Reuse | Cache hit rate (PR #859) | PROJECTED | 0.0004–0.225 |
| L7 | Stream fidelity | SSE done_chunks / total_chunks | MEASURED | 1.0000 |
| L8 | Latency consistency | 1 − CV(latency) | MEASURED | 0.3200 |
| L9 | Completion rate | MsgFinish/MsgStart ratio | MEASURED | 0.9040 |

**Composite QualityScore = 0.7236** (baseline, epoch 161–191, 2,503,595 inferences).

Proposed initial weights: L0=0.10, L1=0.10, L2=0.15, L3=0.10, L4=0.10, L5=0.05,
L6=0.10, L7=0.10, L8=0.10, L9=0.10.

### Key findings

- **L8 is the primary bottleneck**: CV=0.68 (mean=1280ms, σ=876ms). High latency
  variance indicates routing to suboptimal nodes. Quality-weighted routing is the fix.
- **L0 instability**: CV=0.35, peak-to-trough weight loss ~60% over observed epochs.
  Compute capacity needs stable quality incentives, not just PoC nonces.
- **L6 specialization multiplier**: 47.6× improvement at M=12 vs M=571.
  At M=1 (unique model per node): 571× improvement over shared model distribution.
- **L4/L5 do not exist**: no protocol mechanism for usefulness or outcome feedback.
  Participants cannot signal quality; their experience does not improve routing.

---

## Operator Verification (Source A / B / C)

`CacheQualityWeight` depends on self-reported `reuseCount`. Three sources cross-check:

```
[Epoch boundary — Go ticker, 30s interval in main.go]
  ├── Source A: GET /admin/v1/cache/stats      (operator port :9200, atomic reads)
  ├── Source B: on-chain CacheQualityEpochSummary  (self-reported reuseCount)
  └── Source C: Prometheus scraper (GiP #840)  (independent time-series)

Invariant: stats(A).hits ≈ chain(B).reuseCount ± 5%
Divergence > threshold → operator alert; future governance may slash or exclude
```

`MaxWeightFractionBps` (default 3000 bps = 30%) bounds the maximum gain regardless
of reported `reuseCount`. Asymmetric advantage is capped at the protocol level.

---

## Scale constraints (worst-case)

`InMemoryCacheStore` grows unbounded without `max_cache_entries`.

```
Worst-case at mainnet scale (75,016 inferences/epoch):
  Embeddings: 384 dims × float32 = 1536 bytes/entry
  Metadata:   ~400 bytes/entry
  Total:      ~1936 bytes/entry

  10 epochs × 75,016 entries = 750,160 entries × 1.94 KB = 1.46 GB RAM

Required governance bound: max_cache_entries = 50,000
  → peak: 50,000 × 1.94 KB = ~97 MB
  → O(50K) cosine scan per request (acceptable, CPU-only)
  → EvictExpired at epoch boundary keeps store stable over time
```

`max_cache_entries` must be set before enabling `CacheQualityParams.enabled = true`
on mainnet nodes.

Bare-metal and k8s are equally supported: the Go ticker in `main.go` is deployment-agnostic.
DAG orchestration = a separate goroutine in `main.go`. No Prometheus CronJob, no Airflow.

---

## Routing: GetQualityWeightedExecutor

Replaces `GetRandomExecutor` in Phase 4.

```
Current:  executor = random.Sample(activeParticipants)

Proposed: scores[participant] = EpochQualityScore(participant, requestEmbedding)
          executor = WeightedSample(scores)
```

Routing simulation (from measured data):

| | Current (random) | Proposed (quality-weighted) |
|---|---|---|
| Traffic distribution | Uniform 1/M | ∝ QualityScore |
| Completion rate σ | 7.4% (measured) | ~4.4% (projected ↓40%) |
| Mean latency | 1280ms (measured) | ~1088ms (projected ↓15%) |
| GPU saves/epoch (20% specialized) | 0 | 940,698 |
| GPU saves/epoch (50% specialized) | 0 | 2,308,986 |

---

## Semantic Inference Optimization (Phase 5–7)

As the protocol accumulates completed inferences, it builds a **semantic knowledge base**
of execution patterns. This enables active guidance without modifying user content.

### What the protocol does NOT do

- Does not modify prompts
- Does not change model outputs
- Does not store user content beyond the embedding vector (no PII)

### What the protocol provides

- `GET /v1/models/profiles` — per-model quality scores, specialization centroids,
  latency distributions, from last N epochs
- Response headers on cache HIT: `X-Quality-Score`, `X-Task-Archetype`, `X-Suggested-Model`
- Developer SDK (`gonka-sdk`, Phase 7): wraps the API, exposes task routing helpers,
  best-practice request templates based on protocol data

### Architecture (Phase 5)

```
Completed inferences → embed(prompt) → centroid clustering (K=1000)
                                        ↓
                               TaskArchetypeStore
                              /         |          \
                  model_profiles  latency_stats  completion_rates
                                        ↓
                              /v1/models/profiles (read-only)
                              routing hints in response headers
```

Centroid clustering compresses millions of embeddings into K=1000 representatives.
Memory: K × 384 × 4 bytes = 1.5 MB regardless of inference volume. Scalable.

### Developer SDK (Phase 7, separate project)

The SDK is a developer-facing product, not a protocol proposal. It belongs under
Gonka Labs (gonka.gg). Scope:

- OpenAI-compatible client wrapper with automatic model selection
- Task type detection → protocol-optimal routing
- Built-in `X-Inference-Feedback` signaling
- Usage analytics tied to `QualityScore`
- Python + TypeScript packages

Acceptance criteria: any developer using the SDK should achieve ≥ top-quartile
QualityScore without manual configuration.

**Reference implementation (Go middleware layer):**
[`Mayveskii/gonka-quality-middleware`](https://github.com/Mayveskii/gonka-quality-middleware) —
HTTP middleware measuring L6/L8/L9/DX axes on every request through the
[gonkalabs/opengnk](https://github.com/gonkalabs/opengnk) proxy.
Axes tracked: L6 cache hit rate, L8 latency CV, L9 completion rate, DX feedback loop.
Tests: 7/7 PASS (Go 1.22, no GPU, no external deps). Stats endpoint: `GET /quality/stats`.

---

## Proto extension (Phase 1)

Extend `CacheQualityEpochSummary` with additional axes:

```proto
message CacheQualityEpochSummary {
  // existing fields 1–7 (PR #859)
  string participant_address  = 1;
  uint64 epoch_index          = 2;
  int64  cache_reuse_count    = 3;
  int64  original_compute_count = 4;
  uint32 avg_similarity_bps   = 5;
  int64  cache_quality_weight = 6;
  string embedding_model_version = 7;

  // Phase 1 additions
  uint32 completion_rate_bps  = 8;   // L9: MsgFinish / (Finish+Miss+Invalidate)
  uint32 avg_latency_ms       = 9;   // L8: mean request latency this epoch
  uint32 latency_stddev_ms    = 10;  // L8: σ(latency)
  uint32 stream_fidelity_bps  = 11;  // L7: SSE [DONE] received / total × 10000
  int64  feedback_score_sum   = 12;  // L4: Σ (+1/-1) feedback signals
  int64  feedback_count       = 13;  // L4: number of feedback signals
}
```

New `CacheQualityParams` fields (Phase 1):

```proto
// axis_weights[i] = weight for Li in basis points. len=10, sum=10000.
// Default: [1000,1000,1500,1000,1000,500,1000,1000,1000,1000]
repeated uint32 axis_weights      = 8;

// max_cache_entries bounds InMemoryCacheStore growth. Default: 50000.
// At 1.94KB/entry: ~97MB peak. Must be set before enabling on mainnet.
uint64 max_cache_entries          = 9;
```

---

## Implementation phases

| Phase | Scope | Depends on | Effort |
|---|---|---|---|
| 0 | L6 semantic cache | #793 → #703 → #859 | Code complete |
| 1 | Proto extension (fields 8–13 + axis_weights + max_cache_entries) | Phase 0 merged | ~2 days |
| 2 | L7+L8 tracking in QualityReporter | Phase 1 | ~1 day |
| 3 | L4 X-Inference-Feedback header | Phase 1 | ~1 day |
| 4 | GetQualityWeightedExecutor routing | Phase 2+3 | ~3 days |
| 5 | TaskArchetypeStore + centroid clustering | Phase 0 + StatsStorage | ~3 days |
| 6 | /v1/models/profiles + response headers | Phase 4+5 | ~2 days |
| 7 | gonka-sdk (separate repo) | Phase 6 stable | TBD (ref: gonka-quality-middleware) |

---

## k8s specialization reference

For k8s deployments, node specialization (M=1 per model) maximises L6 cache hit rate.

```yaml
# Operator reference: specialize a node to a single model
# Per GiP #816 (Node Manager) and gonka-main/deploy/
env:
  - name: GONKA_MODEL_WHITELIST
    value: "Qwen/QwQ-32B"   # single model → M=1 for this node
  - name: GONKA_CACHE_MAX_ENTRIES
    value: "50000"
  - name: GONKA_CACHE_ENABLED
    value: "true"
```

At M=1, expected hit_rate = 0.27 (30% repeat fraction × 90% non-stream fraction).
GPU saves per epoch: ~20,000 (at 75K inferences/epoch average).
Weight bonus: +30% (MaxWeightFractionBps cap reached).

---

## Evidence baseline

All data reproducible from public endpoints. No private data used.

```
gonka.gg/api/public/stats/network-overview     — network topology (M values per model)
gonka.gg/api/public/stats/participants/summary — epoch-level aggregate stats
gonka.gg/api/public/stats/epochs/list          — per-epoch data
proxy.gonka.gg/v1/chat/completions             — live inference (OpenAI-compatible)

Script: /tmp/gonka_quality_test.py  (L7+L8 measurement, 16 requests, paced)
Script: /tmp/gonka_full_matrix.py   (10-axis composite score calculation)
```

Statistical methodology: `docs/binom-stattest.md` (one-sided binomial test, α=0.05).
Applied to L2 miss rate: n=2,503,595, k=81,360, p0=0.10 → k << critical(251,140) → PASS.

---

## Measurement results (epochs 161–191, public API)

### Network baseline

| Axis | Value | Source |
|------|-------|--------|
| L9 completion avg | 94.33% | MsgFinish/MsgStart ratio, 31 epochs |
| L9 misses avg | 2,624/epoch | direct operator revenue loss |
| L9 misses max | 13,020 (epoch 175) | protocol stress event |
| L8 CV(inferences) | 0.8326 | high load variance → random routing problem |
| L6 reuse current | ~0.0005 | M=571 random routing, no cache active |
| Composite QualityScore | 0.4832 | 4-axis baseline, 2,503,595 inferences |

Network trajectory without intervention: miss rate +0.37pp per 30 epochs,
load/node 872 → 2,384 (+173% over 30 epochs).

### L2 semantic similarity (bookworm, all-MiniLM-L6-v2, dim=384, no GPU)

5 domain tasks, 3 variants each (15 prompt pairs total):

| Threshold (bps) | Hit rate | GPU saves/epoch | Governance action |
|-----------------|----------|-----------------|-------------------|
| 9700 (default)  | 26.7%    | 0               | deploy, validate  |
| 9200            | 40.0%    | 452             | after L2 confirmed |
| 8800            | 60.0%    | 905             | after SDK coverage |
| 8500            | 93.3%    | 1,817           | after 4/5 tasks   |
| 8000            | 100.0%   | 3,634           | after full coverage |

Auth flow domain (T3) hits 0.97 immediately — structured address checks are near-exact.
Each governance step: zero code changes, zero deployments, fully reversible.

### SDK participant interdependence

3 participants, same domain, free-form vs SDK template:

| Mode | Pairwise similarity | L6 activation |
|------|---------------------|---------------|
| Free-form | 0.4911 | none |
| SDK template (DX7) | 0.7960 | at threshold ≤ 0.80 |
| **Delta** | **+0.3049 (+62.1%)** | — |

SDK adoption by one participant raises cache hit probability for all others in the same domain.

### DX→L cascade (full SDK path)

| Step | Action | L8 CV | L9 | Composite | CacheWeight |
|------|--------|-------|----|-----------|-------------|
| Baseline | — | 0.8326 | 0.9433 | 0.4832 | 0.1% |
| +DX0 | autoRegister() | 0.8326 | 0.9461 | 0.4839 | 0.1% |
| +DX2 | estimateTokens() | 0.7493 | 0.9461 | 0.5047 | 0.1% |
| +DX7 + 8500 bps | SDK templates + governance | 0.6369 | 0.9461 | 0.5383 | 2.2% |
| +k8s M=1 | GiP #816 specialization | 0.5000 | 0.9461 | 0.6419 | **30.0%** |

**Total: +32.9% composite score improvement on measured baseline.**

### Worst-case bounds

Parameters: repeat_fraction=5%, stream=50%, M=571, threshold=0.97 (unchanged).

```
hit_rate = 0.05 × (1/571) × (1 - 0.50) = 0.000044 ≈ 0
CacheQualityWeight = 0 → no bonus, no penalty
Feature off by default → ZERO IMPACT
```

Worst case = today's network state. Zero regression, zero protocol debt.

### Remaining measurement gap

Live X-Cache hit rate requires `CacheQualityParams.Enabled=true` on a testnet node for 1 epoch.
This closes real `repeat_fraction` measurement. All other data is measured or worst-case bounded.

---

## Open questions

1. **Weight governance**: who proposes initial `axis_weights`? Amendment process?
2. **L4 incentive**: should feedback submitters receive a nominal reward?
3. **L5 webhook privacy**: what is the data retention model for outcome signals?
4. **SDK governance**: Gonka Labs product vs community repository?
5. **max_cache_entries default**: 50,000 conservative. Preferred bound from hardware profiles?
