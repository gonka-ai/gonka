## PoC-decode

**Problem:** A language model answers a request in two phases: it **reads** the prompt once (the *prefill* phase), then **writes** the reply one token at a time (the *decode* phase), which runs once for every token of the answer. For any real request the writing phase is where the overwhelming majority of the compute goes. Yet the original PoC measures a node's contribution from prefill only: it performs a single prefill forward, generates no tokens, and never runs or checks the decode phase. That leaves the largest and most valuable part of a node's work unproven, and opens the door to configurations tuned to score well on PoC while serving real inference slowly, so a node can be fast at the proof and slow at the work the network actually needs. The proof and the paid job drift apart.

**Goal:** Align PoC with inference. The proof should measure and run like the real workload, both in *what* it proves (the decode phase, where most compute happens) and in *how* it runs (through the same scheduler, KV cache and CUDA graphs as production inference), so that a node cannot be fast at PoC and slow at inference, and its measured contribution matches the job the network is actually paid to do.

**Task:** Extend the PoC procedure to cover decode steps, and run it as ordinary engine requests under CUDA graphs (the same mechanism production decode relies on) so proof performance aligns with real inference performance.

## Theory

**Prefill PoC (the existing scheme, still supported).** The node runs a synthetic prompt through the model once. PoC selects `k_dim=12` dimensions from the final hidden state, forms a small vector, transforms it, and returns it as the proof. It then stops: no decode step is run (`max_tokens = 0`). The prefill derivation is kept byte-identical to `gonka-poc` 0.1.x, so nodes already validated on it are unaffected.

```
  synthetic prompt
    │
    ▼
 ┌─────────┐
 │ prefill │
 └────┬────┘
      │ hidden state
      ▼
  pick 12 dims ──► transform ──► PROOF (one vector)
      │
      ▼
  stop  (decode phase is never run)
```

**PoC-decode** carries the same idea into every decode step, using a sphere.

*Setup.* A fixed **codebook** of 16 points on the unit sphere in 256 dimensions is shipped with the plugin (`sphere_codebook.pt`). The points are spread as far apart as possible: they form a regular simplex, so every pair has the same cosine, −1/15. The codebook is frozen and pinned by its SHA-256; every node loads the identical bytes and refuses to run if they differ. It is not regenerated per block. What changes every round is *which* hidden-state dimensions are read and what is fed into the model, both seeded from the block (see below).

*Inputs.* PoC rows carry no real text. The prefill is a synthetic prompt of `seq_len = 256` embeddings seeded by `(block_hash, public_key, nonce)`. Each decode step's input embedding is also synthetic, seeded by the nonce, the step index and the previous step's point. PoC never samples a token and never enters the sampler or output path.

*Seeded transforms.* Two changes are applied to PoC rows only; chat rows in the same forward pass through untouched:

- **Householder reflection after every decoder layer**, with one reflection vector per layer seeded by `(block_hash, layer_id)`. This makes the computation round-specific, so results cannot be precomputed.
- **Seeded MoE routing**: on MoE models the router's logits for PoC rows are replaced by a deterministic expert choice derived from `(block_hash, nonce, layer, step)`. The prover must therefore hold and run every expert, and routing stops depending on tiny numeric differences in the hidden state, which would otherwise dominate honest cross-GPU disagreement.

*Starting from prefill.* After the prefill forward, the final normed hidden state of the last position is taken. `sphere_dim = 256` of its dimensions are selected by a seeded random pick, the resulting vector is normalised onto the sphere, and the nearest of the 16 codebook points (highest cosine) is recorded as **k0**. This first point connects the reading phase to the decode chain that follows.

*Each decode step.* The same operation runs on that step's final hidden state: select 256 dimensions, project onto the sphere, snap to the nearest point. The chosen point `k(i)` then seeds both **the input embedding** of step `i+1` and **which dimensions** step `i+1` reads.

Because each step's input and selection depend on the previous step's point, and each step attends over the KV cache written by all earlier steps, the steps interlock and must be computed in order. There is no shortcut that skips the actual decode.

```
  synthetic prompt (256 embeddings)
    │
    ▼
 ┌─────────┐   hidden
 │ prefill │──────────► pick 256 dims ──► project on sphere ──► snap ──► k0
 └─────────┘                                                             │
      ┌──── k(i) seeds step (i+1)'s input embedding and its dim pick ────┘
      ▼
 ┌──────────┐  hidden
 │ decode 1 │──────────► pick ──► project ──► snap ──► k1
 └────┬─────┘
      ▼
 ┌──────────┐  hidden
 │ decode 2 │──────────► pick ──► project ──► snap ──► k2
 └────┬─────┘
      ▼
     ...  ─────────────────────────────────────────────► k256

  PROOF    = trajectory [k0, k1, … , k256]   (257 ids in 0..15)
  OPTIONAL = pre-snap vector per step        (off by default; evidence only)
```

### Why sample this way, instead of comparing plain inference

A model's plain output is a weak proof on its own: it is short, easy to copy, and reveals nothing about whether the internal computation was actually performed. K-trajectory sampling turns the run *itself* into the evidence. It is a **reproducible, uniform, chained randomization** run through the same engine as ordinary inference, chosen for three properties:

- **Reproducible.** The codebook is identical on every node, and the inputs, reflections, routing and sampled indices are derived deterministically from the block and nonce, so an honest validator running the same model obtains the same trajectory up to rare boundary flips (see Validation).
- **Uniform.** Randomizing *which* hidden-state dimensions are read, instead of fixing them, makes the chosen points come out near-uniform across the codebook. There is no small, predictable target an attacker could reproduce cheaply without doing the real work.
- **Chained.** Because each step depends on the previous step's point, the trajectory has to be produced in sequence by actually running the model step by step. It cannot be precomputed, cached, or shortcut.

Together these make the trajectory a fingerprint of the genuine computation: cheap to check and hard to fake.

An interactive 3D sphere plot is available [here](https://axeltec-software.github.io/ReportsHelper/poc-decode/sphere_projection.html), with example code in `notebooks/poc-sphere-projection.ipynb`. K-point selection statistics across different block hashes are available as a [chart](https://axeltec-software.github.io/ReportsHelper/poc-decode/nearest_k_point_dist.html). *(These plots were produced on the August prototype; the codebook has since been frozen.)*

**Final output:** per nonce, the array of 257 selected point ids (`k_points_steps`). This sequence is the proof-of-computation for the whole decode. The decode artifact leaves the prefill-style `vector_b64` empty. When a node runs with `poc_vector_artifacts` enabled, it also returns the pre-snap vector of every step (`sph_values_steps`); see below.

### Validation

The method operates in two modes: generation and validation.

- **Generation mode** runs the pipeline above and produces the trajectory.
- **Validation mode** replays it *teacher-forced*: the request carries the prover's `k_points_steps`. At each step the validator computes its own snap, compares it with the prover's claim, and then continues the chain with the **prover's** point, so a single disagreement does not derail the rest of the run.

**Snap margin.** A disagreement alone does not prove fraud. When the validator's query sits close to the boundary between two cells, slightly different floating-point rounding on another GPU flips the snap. So every disagreement is scored by how much worse the claimed cell is than the validator's own best:

```
margin = s_best − s_claimed          where s_k = cos(q, c_k)
```

- `0`: the validator agrees with the claim.
- small: the claim is the validator's runner-up, a boundary flip (jitter).
- large: the claim lies in a clearly different cell, which means a different computation.
- `2.0`: the claimed id is outside the codebook.

**Per nonce.** A nonce's distance is the largest margin among its disagreeing steps. The nonce counts as a **mismatch** when it has at least one disagreement and that distance exceeds τ (`stat_test.dist_threshold`, sent by the chain with the validation request).

**Per node.** Across the sampled nonces (200 per validation request), the same one-sided binomial test as the prefill scheme decides: is the number of mismatching nonces higher than an honest rate of `p_mismatch` would explain? Fraud is declared when the p-value falls below α.

| Parameter (chain, upgrade v0.2.17) | Value |
| --- | --- |
| τ, MiniMax-M2.7 | 0.0395 |
| τ, DeepSeek-V4-Flash | 0.027 |
| τ, GLM-5.3-Flash | 0.017 |
| `p_mismatch` | 0.1 |
| α (`p_value_threshold`) | 0.05 |
| `seq_len` / `max_tokens` | 256 / 256 |

τ is set per model because the size of honest boundary flips depends on the architecture. For scale: on this codebook, a random direction in 256 dimensions has a median gap of about 0.024 between its best and second-best score.

**Optional vector channel.** With `poc_vector_artifacts` enabled, both sides also exchange the pre-snap vector of every step, and the validator reports a per-step cosine-distance score (`vector_score`). This is the same kind of continuous vector the prefill scheme already relies on, one per decode step instead of one per nonce. It is attached as evidence only: the verdict is always taken from the k-trajectory. It is off by default.

### Chain integration

The scheme is chosen on chain for the whole network (`PocParams.poc_scheme`: PREFILL or DECODE) and frozen for the epoch, so all nodes in an epoch mine and validate under the same scheme. Moving from prefill to decode is a governance parameter change; the switch takes effect at the next epoch boundary and keeps the previous coefficients. One MLNode serves both schemes, selected per request by `params.scheme`.


## Fraud resistance

- The hidden-state indices read at each step are chosen by a seed that includes the previous step's point, which yields a near-uniform point distribution and removes any fixed target an attacker could aim at.
- Inputs, per-layer reflections and MoE routing are seeded by the block, so no trajectory can be computed before the round starts.
- Every step reads the KV cache written by the prefill and all earlier steps. Skipping steps or replacing decode with a prefill pass changes the attention inputs and the trajectory.
- Hardcoding the point instead of deriving it from real computation was verified not to be viable: hardcoded variants consistently diverge from the honest pipeline.
- Even two honest runs never match to the last bit, because different GPUs round the arithmetic slightly differently. Such differences only flip a snap when the query sits on a cell boundary, and then the margin is small. The margin rule forgives these flips, while a cheaper model or lower precision moves the query into a clearly different cell.
- A claimed id outside the codebook gets margin 2.0. A nonce whose computation produced non-finite values is dropped from the artifacts; in validation, any such nonce makes the validator abstain from the whole vote instead of counting as a pass.


## Results

> **Status.** The tables below were measured in August 2026 on the vLLM 0.20 prototype with **MiniMax-M2**, using the earlier per-step mismatch metric (share of decode steps whose point disagrees, threshold ≈ 11.98%). The verdict now uses the per-nonce margin rule above, and production runs vLLM 0.30 with MiniMax-M2.7, DeepSeek-V4-Flash and GLM-5.3-Flash. The tables still show how honest and fraudulent work separate, but they are not the current acceptance criterion and should be re-run under the margin rule.

The PoC-decode procedure is model-agnostic. This run spans four GPU platforms (B300, H100, H200, A100):

- **Honest setup:** the same model on *different* hardware (e.g. a run produced on one GPU type validated on another).
- **Fraud setup:** a different quantization of the model (AWQ) validated against the reference model.

### Separation (August, per-step metric)

| Metric | Value |
| --- | --- |
| Honest mismatch rate | 0% to ~10.4% |
| Fraud mismatch rate | ~13.6% to ~14.2% |
| Acceptance threshold (then) | ≈ 11.98% |

Per pair, the average k-trajectory mismatch (%) with each validator (rows) re-checking each prover (columns):

**Honest (same model):**

| validator ↓ / prover → | B300 | H100 | H200 | A100 |
| --- | --- | --- | --- | --- |
| **B300** | 0.00 | 7.76 | 8.05 | 10.41 |
| **H100** | 7.50 | 0.09 | 7.40 | 9.92 |
| **H200** | 8.07 | 7.41 | 0.08 | 10.03 |

**Fraud (cheaper model, AWQ prover):**

| validator ↓ / prover → | B300 | H100 | H200 |
| --- | --- | --- | --- |
| **B300** | 14.20 | | 13.90 |
| **H100** | 14.00 | 13.57 | 13.84 |
| **H200** | 14.08 | 13.73 | 13.67 |

The diagonal is same-hardware validation and sits near 0%. Every honest cell stays below the threshold and every fraud cell above it.

A later MiniMax-M2.7 campaign (B300 ↔ H200, after seeded routing switched to a single ladder base for all models) measured honest cross-hardware 7.5–7.6% against fraud 12.2–12.4%, a 4.8 pp gap on the same per-step scale.

Golden reference trajectories for the current scheme (200 nonces each, 257 steps per nonce) ship with the MLNode validation benchmark: `deepseek-ai-deepseek-v4-flash-0731-decode.json`, `minimaxai-minimax-m2.7-decode.json`, `zai-org-glm-5.3-flash-decode.json`.

### Performance and fairness across platforms

PoC runs in its most performant configuration, the same one production inference uses: **CUDA-graph execution** and **large batches**. This is a fairness measure, not only a speed one. When the proof runs as efficiently as real inference, there is no gap between "being fast at PoC" and "being fast at the actual job," so a node cannot tune its setup to score well on the proof while serving inference slowly.

In the current implementation every PoC nonce is an ordinary engine request: it goes through vLLM's scheduler, gets KV blocks from the same KV-cache manager as chat (no reserved blocks), and runs one decode step per scheduler step. All PoC transforms (synthetic embeddings, reflections, seeded routing, the snap) run inside the captured graph. Models that run without `torch.compile` keep a second graph set for batches containing PoC rows, so chat graphs carry no PoC overhead. During mining, chat gets 503. During validation, while PoC rows are live, chat work is paused and resumes afterwards; a PoC row that needs slots held by paused chat preempts the lowest-priority chat request. Decode mining keeps up to `batch_size` nonces in flight and starts them in staggered groups, so artifacts are published continuously instead of in whole-round bursts.

August measurement (v0.20 prototype, MiniMax-M2), as the **overhead ratio** (proof throughput divided by the node's normal chat throughput):

| Platform | Proof (nonce/s) | Chat (req/s) | Overhead ratio |
| --- | --- | --- | --- |
| 1×B300 | 14.86 | 19.16 | 0.775 |
| 4×H100 | 17.62 | 24.10 | 0.731 |
| 4×A100 | 10.65 | 14.93 | 0.713 |
| 2×H200 | 12.93 | 19.84 | 0.652 |

The ratios span only **×1.19** from lowest to highest, so the proof leans on every platform about equally and no hardware is unfairly favored.

A recent data point on the current stack: 2×H200, 300 s of decode mining, MiniMax-M2.7 at batch 608 produced 8,512 nonces (≈28 nonce/s); DeepSeek-V4-Flash at batch 1024 produced 5,888–6,144.

A core deliverable of this project was re-building PoC as a **CUDA-graph-based algorithm**. Previously the proof ran step-by-step (eager); now the whole PoC path (prefill and decode) executes inside CUDA graphs, exactly as production inference does. On the prototype this sped a node up by up to **~7×** on chat and **~3.5×** on the proof path (measured on B300), which is what lets the proof run at inference speed rather than competing with it.


## Open items

- **τ in the golden references vs. on chain.** The shipped decode references carry τ 0.05 (DeepSeek, MiniMax) and 0.025 (GLM), while upgrade v0.2.17 sets 0.027, 0.0395 and 0.017. One of the two should be brought in line.
- **Ladder base for seeded routing** (one value, 100, for all models) is marked in the code as pending sign-off by the scheme owner.
- **Results under the margin rule.** The cross-hardware honest/fraud tables should be re-measured with the per-nonce margin verdict on vLLM 0.30.
- **Plugin README.** The `decode/vlm030` README still requires `--enforce-eager` for the PoC forward, which contradicts CUDA-graph execution.


## Changes since the August version of this document

| Topic | August (v0.20 prototype) | Now (v0.30, `gonka-poc` 0.2.x) |
| --- | --- | --- |
| Codebook | points derived from the block | 16 fixed points in 256-dim, frozen file pinned by SHA-256 |
| Verdict | share of mismatching steps vs ≈11.98% | per-nonce margin > τ, then binomial test (p_mismatch 0.1, α 0.05) |
| Threshold | one value | τ per model on chain |
| Pre-snap vector | recorded per token as a fallback | optional (`poc_vector_artifacts`), evidence only |
| Execution | separate PoC loop in CUDA graphs | PoC nonces are engine requests: shared scheduler, KV cache and graphs |
| Chat during PoC | shared | 503 during mining, paused during validation |
| Scheme selection | decode only | prefill or decode for the whole network, frozen per epoch on chain |
| MoE routing | route window 256 | seeded contiguous expert run, single ladder base |
