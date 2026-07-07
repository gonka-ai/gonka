> Mirrored from vLLM `docs/DETERMINISTIC_SAMPLING_CONTRACT.md` (authoritative).
> Keep in sync by contract_version; the golden vectors in
> `testdata/conformance_vectors.json` are the executable form.

# Deterministic Sampling — Cross-Language Contract (v1.0.0)

This document pins the **exact, language-agnostic contract** for turning a
position's logprobs + sampling params + RNG seed into a sampled token. Three
parties must produce **bit-identical** results from it:

1. the vLLM **executor** (produces the artifact),
2. the vLLM **Python validator** (`vllm/validation_sampling.py`),
3. the gonka **Go validator** (`decentralized-api/internal/validation`).

Any divergence between parties turns an honest inference into a false fraud, so
every clause below is normative. The golden vectors in
`tests/v1/validation/conformance_vectors.json` are the executable form of this
contract; both the Python and Go sides MUST reproduce them exactly.

> Scope: this contract covers `logprobs → integer weights → sampled token` and
> the RNG. It does **not** cover how the seed *string* is composed
> (that is seed hardening / S1, tracked separately).

---

## 0. Versioning

- `contract_version = "1.0.0"`. Any change to a clause below is a version bump.
- The version is embedded in `conformance_vectors.json` and SHOULD be recorded
  in the artifact so a validator can reject an unknown-contract artifact as
  *version-unsupported* rather than *fraud*.

## 1. Probability encoding — canonical string, produced once

- The artifact carries each logprob as a **canonical decimal string**, not a
  JSON float. The producer (executor) performs the **only** float→string
  conversion, exactly once, as:

  ```
  canonical_str = repr(float64_value)
  ```

  where `float64_value` is the model's logprob widened to IEEE-754 binary64.
  `repr` is CPython's shortest round-tripping representation.
- Consumers (both validators) parse with `Decimal(canonical_str)` and **never**
  format a float themselves. In particular the Go side never runs Ryu / `strconv`
  on a float — it only ingests the string. This removes the Python(`repr`) vs
  Go(Ryu) shortest-float disagreement (golang/go#17997) by construction.
- Rationale: `repr(-0.05) == "-0.05"`, but the same value as a float32 widened
  to float64 is `"-0.05000000074505806"`. The canonical string therefore
  depends on the exact binary64 bits; pinning one producer + one function is the
  only way both sides agree.

## 2. Decimal context

All Decimal arithmetic runs under a **local** context (never the process-global
one):

- `prec = 10`
- `rounding = ROUND_HALF_EVEN` (IEEE 754-2008 default)

Every operation in the pipeline (`.exp()`, division, `.to_integral_value()`,
comparisons) MUST execute inside this context.

## 3. Token iteration order

All iteration, summation, and the final weight-list → categorical-index mapping
use tokens **sorted by token-ID string, lexicographically** (`sorted(keys)`).
This eliminates accumulation-order ambiguity and pins the index space that the
RNG samples over.

## 4. Pipeline (fixed order)

Given `{token_id_str: logprob_str}`, `temperature` (string, `> 0`), and optional
`top_p` / `top_k` / `min_p`:

1. **Temperature scale:** `scaled[t] = Decimal(logprob[t]) / Decimal(temperature)`.
2. **Softmax (max-shifted):** `m = max(scaled)`; `exps[t] = (scaled[t] - m).exp()`;
   `probs[t] = exps[t] / sum(exps)`.
3. **top_k:** if `top_k is not None and top_k < len`, keep the `top_k` tokens by
   probability (desc), then re-sort survivors by token-ID string.
4. **top_p:** if set, sort by probability desc, accumulate; keep tokens until the
   running sum `>= top_p` **including** the token that crosses the threshold.
5. **min_p:** if set, `threshold = max(probs) * min_p`; keep `probs[t] >= threshold`.
   If that keeps nothing, fall back to the single argmax token.
6. **Renormalize** over survivors: `norm[t] = probs[t] / sum(survivor probs)`.
7. **Quantize:** `w[t] = int((norm[t] * 65536).to_integral_value())`
   (`WEIGHT_SCALE = 2^16 = 65536`).
8. **Residual fix:** `residual = 65536 - sum(w)`; add it to the token with the
   largest `(weight, token_id_str)` tuple. Result sums to exactly `65536`.

## 5. RNG — `Sha256CounterRNG`

- State: `seed_bytes` (UTF-8 of the seed string) and a `counter` starting at `0`.
- Each draw: `u64 = int.from_bytes(SHA256(seed_bytes || be_u64(counter))[:8], "big")`,
  then `counter += 1`. `be_u64` is the 8-byte big-endian encoding.
- Reference vector: `iter_u64("reference_seed_v1", 5)[0] == 4286832458236889005`.

## 6. Categorical sampling

- `total = sum(weights)`.
- If `total <= 0`: **raise / log an error** — do not silently return a token.
  (A zero total in zero-tolerance validation signals an upstream bug.)
- Unbiased draw: `limit = 2^64 - (2^64 % total)`; repeatedly take `u64` until
  `u64 < limit`; `r = u64 % total`.
- Linear scan the cumulative weights in token-ID-string order; return the first
  index whose cumulative sum exceeds `r`.

## 7. Greedy / temperature 0

`temperature == 0` (greedy / argmax) bypasses this pipeline and the RNG
entirely. Stage-1 sequence checking provides **no** protection at temperature 0;
only the Stage-2 distance check defends there. Validators MUST branch on
`temperature is not None and temperature > 0`, never on a falsy-zero test.

---

## 8. Chain-bound seed derivation (independently versioned)

The RNG seed *string* is not composed by §1–§7; it is derived by
`derive_chain_bound_seed(user_seed, inference_id_from_chain)` (from
gonka-ai/vllm#56), binding Stage-1 replay to chain provenance so it cannot be
ground from request-controlled material. Its own domain tag versions it:
`gonka-deterministic-sampling-v1`.

- Framing: `SHA256(tag || "\nuser_seed_len=" || len || "\n" || str(user_seed)
  || "\ninference_id_len=" || len || "\n" || inference_id)`, all UTF-8, byte
  lengths (not code points). Returns the lowercase hex digest, which is then the
  seed string fed to `Sha256CounterRNG`. **No raw concatenation** (ambiguous
  across `(4,"2x")` vs `(42,"x")`).
- Fail closed — the accept/reject boundary is language-invariant:
  `inference_id_from_chain` must be **printable ASCII (0x21..0x7E)**, non-empty,
  ≤256 chars (stricter than a `strip()` check, whose whitespace set differs per
  runtime); `user_seed` must be an exact int in the **signed 64-bit range**.
- Covered by the `seed_derivation` block in `conformance_vectors.json` (accept
  digests + reject cases); the Go validator reproduces both.

## Conformance

`scripts/gen_conformance_vectors.py` emits `conformance_vectors.json` from the
reference Python implementation. `tests/v1/validation/test_conformance_vectors.py`
asserts the Python pipeline still reproduces the committed vectors (drift guard).
The Go validator MUST pass the same vectors before it can be trusted for
cross-language replay: sampling parity lives in the `detsample` package
(`TestPipelineWeightsMatch` / `TestPipelineTokenMatch`) and seed parity in
`TestChainBoundSeedAccept` / `TestChainBoundSeedReject`.
