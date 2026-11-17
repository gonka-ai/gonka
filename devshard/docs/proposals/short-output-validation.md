# Short-output validation

Status: implemented; ships as a new devshard version. Issue: gonka#1955.

## Problem

Before this version, every devshard request is rewritten with `min_tokens = 64` (and `max_tokens` raised to at least 64), and `stop_token_ids` are stripped. The floor exists because the legacy validation score cannot judge short outputs, but it breaks tool calls, classifiers and any short answer: the model is forced to keep generating past its natural end.

The legacy score is `Σd / max(100, N)` over the N output positions, where `d` is the per-position logprob distance. Below 100 positions the missing slots count as perfect agreement, so a short output is diluted: one position contributes less than 0.5, so an output with `N ≤ 2·(1−T)·100` passes the chain threshold `T` whatever logprobs it claims. Removing the floor without changing the score would reopen model substitution on short outputs.

## Design

Short-output validation is the only behaviour of the new devshard version: one request rewrite and one scoring policy (`validation.DefaultShortOutputScoringPolicy`), plus the gateway and host changes that let a short reservation exist.

### Request rewrite

The gateway, the executor and the validator replay rewrite the request the same way (`completionapi.EnforceTokenBudget`): `max_tokens` is not raised, no `min_tokens` floor is injected, and only a `min_tokens` the caller sent survives, clamped to `max_tokens`. `stop_token_ids` are kept instead of stripped, after a vocabulary check (below).

A grammar-constrained request (`completionapi.GrammarConstrained`) carries no `min_tokens`, not even the caller's: once the grammar allows only EOS, masking EOS leaves every logit at -inf.

The validator replaces the old "output shorter than the floor" check with two checks: an output shorter than the caller's own `min_tokens` fails, and an output that continues past a requested stop token fails.

### Scoring policy

The score goes through three checks, all on output positions only. Each position's distance uses at most as many executor `top_logprobs` as the validator returned, so a padded width cannot shift the score; vLLM serializes a −inf logprob as −9999. A failed check rejects the output regardless of the threshold; otherwise the mean-distance check's similarity is compared with the chain's per-model `validation_threshold` (`similarity > T`).

- **Sampler-support check** (processed logprobs only): counts output tokens to which the validator's processed sampler gives no probability (logprob ≤ −9999 or NaN). More than `max(1, ⌈0.03·N⌉)` rejects. In processed mode the validator scores the enforced token under the genuine sampler, so such a token could not have been produced by it. That budget holds for samplers at least as wide as the calibration corpora's (`top_k` off or ≥ 40, `top_p` off or ≥ 0.95, no `min_p`, and no `top_p` below 1 under a temperature below 0.7), whose support ends on tokens with almost no mass. A narrower request (`ScoringPolicy.ForRequest`) gets `max(1, ⌈0.5·N⌉)`: there the support ends on a token with real mass (with `top_k = 1` it is the argmax alone), and GPU drift moves that boundary at a few percent of positions, so honest tokens fall outside it. Half the output still rejects a fabricated output under a collapsed sampler, which is out of support at every position.
- **Per-position ceiling check**: counts output positions whose distance exceeds 0.30. More than `⌊0.02·N⌋` rejects; in processed mode at least 3 are always allowed, because a top-k entry clamped to −9999 on one side alone pushes an honest position over the ceiling. The check is never averaged, so a few fabricated positions cannot hide in a long output.
- **Mean-distance check**: `mean = max(Σd/N − 0.10/√N, Σd/max(100, N))` over all N output positions, with no selection of positions. The similarity is `1 − mean`. Below 100 positions the first term is the true mean less an allowance for the noisier honest mean of a short output; the phantom slots are gone, which is the fix. The second term is the legacy distance, so the check is never more lenient than legacy at any length, and for N ≥ 100, where legacy already divides by N, it is the legacy score exactly. The existing per-model chain thresholds keep the scale they were set on and no governance change is needed.

### Per-model enforcement

The per-position ceiling and sampler-support checks reject only on the models in `OutputChecksEnforcedModels`: `deepseek-ai/DeepSeek-V4-Flash-0731` and `zai-org/GLM-5.3-Flash`, the two models with calibration data. On every other model they run log-only: the validator logs `per-position ceiling check would fail (log-only): ...` or `sampler-support check would fail (log-only): ...`, and the mean-distance check alone decides. The validator resolves the list from the request's chain model id, the same key it uses for the threshold lookup. Adding a model to the list needs a release.

### Reservation

A reservation must be at least 1 token (`state.MinReservationTokens`). `applyStartInference`, the executor's receipt check and the verifier's refused-timeout check use the same bound, so a short reservation is signed, executed and can time out like any other. Nothing new is stored in the session: no proto field, config field or snapshot field changes.

### Gateway `stop_token_ids` vocabulary check

With `min_tokens > 0` the engine indexes logits by the stop ids, so an out-of-range id crashes the node rather than being ignored. The gateway checks every id against the routed model's `vocab_size` in the current epoch, resolved through `common/vocabulary` (the same Hugging Face `config.json` resolver the executor uses, over `EpochGroupData`). In-vocab ids are forwarded. Out-of-range, negative or non-integer ids, an unresolvable vocabulary, or epoch 0 get a 400 (`invalid stop_token_ids: ...`) and are never forwarded. The executor repeats the check before the ML node; the validator rejects ids outside a known vocabulary and replays without them when its own vocabulary is unknown.

## Calibration

Method: the real Go `CompareLogitsWithPolicy` scores replay corpora of real inferences at the live on-chain thresholds, in `processed_logprobs` (the chain's current mode) and `raw_logprobs`. Each corpus is 1000 prompts with an honest executor and one or two substituted executors on one hardware pair: DeepSeek-V4-Flash-0731 (B300 executor, H200 validator; NVFP4 and old-checkpoint substitutes) and GLM-5.3-Flash (2×B300 executor, 4×H200 validator; NVFP4 substitute). An independent re-implementation from the dumped per-position distances agrees on every verdict (0 mismatches over 10,000 rows, largest distance difference 2.5e-16).

Live thresholds: MiniMax-M2.7 0.922, DeepSeek-V4-Flash-0731 0.900, Kimi-K2.6 0.900, GLM-5.2-FP8 0.750, GLM-5.3-Flash 0.951.

Results, honest rejected / fraud caught, in %:

| model @ live T | mode | slice (honest / NVFP4 / old-ckpt) | legacy | short-output validation | same, output checks log-only |
|---|---|---|---|---|---|
| DeepSeek-V4-Flash-0731 @0.900 | processed | all (1000/1000/1000) | 0.0 / NVFP4 0.0 / old 25.3 | 0.0 / 3.3 / 84.4 | 0.0 / 0.2 / 31.5 |
| | processed | N<64 (185/181/220) | 0.0 / 0.0 / 0.45 | 0.0 / 3.9 / 44.5 | 0.0 / 1.1 / 26.4 |
| | raw | all (1000/1000/1000) | 0.0 / 0.0 / 20.8 | 0.0 / 0.1 / 33.2 | 0.0 / 0.1 / 32.6 |
| | raw | N<64 (181/186/214) | 0.0 / 0.0 / 0.0 | 0.0 / 0.5 / 50.9 | 0.0 / 0.5 / 48.6 |
| GLM-5.3-Flash @0.951 | processed | all (1000/1000) | 0.6 / NVFP4 72.6 | 0.6 / 78.4 | 0.6 / 72.7 |
| | raw | all (1000/1000) | 0.0 / 98.9 | 0.0 / 99.0 | 0.0 / 99.0 |

The per-position ceiling and sampler-support checks reject no honest output in any model, mode or slice. They reject 835 of 1000 DeepSeek processed old-checkpoint outputs, 31 of 1000 DeepSeek processed NVFP4, 28 of 1000 DeepSeek raw old-checkpoint, 611 of 1000 GLM-5.3 processed NVFP4 and 12 of 1000 GLM-5.3 raw NVFP4. The six honest GLM-5.3 processed rejects (0.6 %) are the mean-distance check on outputs of 257+ tokens, the same six legacy rejects at the same threshold. The log-only column is what the three uncalibrated models get.

By output length N, the same verdicts in % (legacy → short-output validation); "thin" marks fewer than 30 honest outputs. Summed over the buckets they reproduce the all-length rows above exactly. Honest rejection equals legacy in every bucket: 0 everywhere except GLM-5.3 processed N ≥ 257 (0.7 → 0.7, the same outputs). Catch is never below legacy in any bucket. Up to 64 tokens the rule catches old-checkpoint substitutes on DeepSeek where legacy caught almost none (23–64 % processed, 47–56 % raw); in processed mode the ceiling and sampler-support checks add catches at every length; NVFP4 gains are small (at most 8.6 points) and in raw mode appear only at 1–8 tokens. GLM-5.3 has too few outputs below 129 tokens to say anything there.

| model | mode | N | n honest | honest rejected | n NVFP4 | NVFP4 caught | n old ckpt | old ckpt caught |
|---|---|---|---:|---:|---:|---:|---:|---:|
| DeepSeek-V4-Flash-0731 | processed | 1-8 | 27 (thin) | 0.0 → 0.0 | 28 | 0.0 → 3.6 | 26 | 0.0 → 23.1 |
| DeepSeek-V4-Flash-0731 | processed | 9-16 | 36 | 0.0 → 0.0 | 38 | 0.0 → 0.0 | 50 | 0.0 → 24.0 |
| DeepSeek-V4-Flash-0731 | processed | 17-32 | 60 | 0.0 → 0.0 | 58 | 0.0 → 8.6 | 57 | 0.0 → 43.9 |
| DeepSeek-V4-Flash-0731 | processed | 33-64 | 62 | 0.0 → 0.0 | 57 | 0.0 → 1.8 | 88 | 1.1 → 63.6 |
| DeepSeek-V4-Flash-0731 | processed | 65-128 | 101 | 0.0 → 0.0 | 106 | 0.0 → 8.5 | 121 | 24.8 → 84.3 |
| DeepSeek-V4-Flash-0731 | processed | 129-256 | 165 | 0.0 → 0.0 | 156 | 0.0 → 8.3 | 188 | 33.5 → 96.8 |
| DeepSeek-V4-Flash-0731 | processed | 257+ | 549 | 0.0 → 0.0 | 557 | 0.0 → 0.7 | 470 | 33.8 → 98.1 |
| DeepSeek-V4-Flash-0731 | raw | 1-8 | 28 (thin) | 0.0 → 0.0 | 29 | 0.0 → 3.4 | 27 | 0.0 → 55.6 |
| DeepSeek-V4-Flash-0731 | raw | 9-16 | 38 | 0.0 → 0.0 | 42 | 0.0 → 0.0 | 48 | 0.0 → 52.1 |
| DeepSeek-V4-Flash-0731 | raw | 17-32 | 59 | 0.0 → 0.0 | 52 | 0.0 → 0.0 | 64 | 0.0 → 53.1 |
| DeepSeek-V4-Flash-0731 | raw | 33-64 | 57 | 0.0 → 0.0 | 64 | 0.0 → 0.0 | 76 | 0.0 → 47.4 |
| DeepSeek-V4-Flash-0731 | raw | 65-128 | 102 | 0.0 → 0.0 | 100 | 0.0 → 0.0 | 126 | 44.4 → 55.6 |
| DeepSeek-V4-Flash-0731 | raw | 129-256 | 161 | 0.0 → 0.0 | 155 | 0.0 → 0.0 | 198 | 45.5 → 45.5 |
| DeepSeek-V4-Flash-0731 | raw | 257+ | 555 | 0.0 → 0.0 | 558 | 0.0 → 0.0 | 461 | 13.4 → 13.4 |
| GLM-5.3-Flash | processed | 1-8 | 0 (thin) | – | 0 | – | – | – |
| GLM-5.3-Flash | processed | 9-16 | 0 (thin) | – | 0 | – | – | – |
| GLM-5.3-Flash | processed | 17-32 | 1 (thin) | 0.0 → 0.0 | 1 | 0.0 → 0.0 | – | – |
| GLM-5.3-Flash | processed | 33-64 | 2 (thin) | 0.0 → 0.0 | 1 | 0.0 → 0.0 | – | – |
| GLM-5.3-Flash | processed | 65-128 | 16 (thin) | 0.0 → 0.0 | 14 | 42.9 → 50.0 | – | – |
| GLM-5.3-Flash | processed | 129-256 | 76 | 0.0 → 0.0 | 73 | 41.1 → 47.9 | – | – |
| GLM-5.3-Flash | processed | 257+ | 905 | 0.7 → 0.7 | 911 | 75.7 → 81.4 | – | – |
| GLM-5.3-Flash | raw | 1-8 | 0 (thin) | – | 0 | – | – | – |
| GLM-5.3-Flash | raw | 9-16 | 0 (thin) | – | 0 | – | – | – |
| GLM-5.3-Flash | raw | 17-32 | 0 (thin) | – | 1 | 0.0 → 0.0 | – | – |
| GLM-5.3-Flash | raw | 33-64 | 3 (thin) | 0.0 → 0.0 | 1 | 0.0 → 0.0 | – | – |
| GLM-5.3-Flash | raw | 65-128 | 16 (thin) | 0.0 → 0.0 | 15 | 73.3 → 80.0 | – | – |
| GLM-5.3-Flash | raw | 129-256 | 71 | 0.0 → 0.0 | 68 | 98.5 → 98.5 | – | – |
| GLM-5.3-Flash | raw | 257+ | 910 | 0.0 → 0.0 | 915 | 99.6 → 99.6 | – | – |

Why these constants: the ceiling 0.30 sits above the largest honest per-position distance seen on three MiniMax-M2.7 fp8 executor/validator pairs (0.224) and below a lazy-proof executor (0.31–0.50). A tolerance of 0.02·N rather than 0.01·N removes the 2.1 % honest GLM-5.3 processed ceiling rejects. An allowance of 0.10 rather than 0.05 brings DeepSeek processed short honest rejects from 1.1 % to 0. Applied at every length, the allowance loosened the check where legacy already scores a true mean: DeepSeek raw old-checkpoint catch fell from 45.5 % to 17.7 % at 129–256 tokens and GLM-5.3 NVFP4 from 41.1 % to 32.9 %; the legacy term restores those rows. Dropping the allowance only from N = 100 on was rejected: the score jumps by up to 0.015 at N = 100, and 6 honest GLM-5.3 outputs truncated to 99 and 100 positions (5 processed, 1 raw) pass at 99 and fail at 100 where legacy does not flip. Tapering it linearly to 0 between 64 and 100, or as `0.10·(1/√N − 1/10)`, rejects more honest GLM-5.3 truncations of 60–99 positions than legacy (20 and 59 against 14 in processed mode). A mean over selected "informative" positions was rejected: it is 2.3–2.5× the all-position mean on DeepSeek and GLM-5.3 (5.7× on Hy3), so it would need new per-model thresholds and rejected 62.5 % of honest GLM-5.3 processed outputs at the live threshold.

Narrow samplers: the corpora were sampled with `top_k = 40, top_p = 0.95` at temperature 0.7, so the 3 % sampler-support budget is calibrated for that sampler only. A local reproduction (Qwen2.5-0.5B-Instruct bf16 on MLX; decode-time logprobs as the executor, a prefill of prompt and output as the validator, vLLM's processed masking; 100 honest 30–32-token answers per sampler at temperature 0.7, scored with `CompareLogitsWithPolicy` under the GLM-5.3-Flash policy) rejected 10 of 100 honest answers under the 3 % budget with `top_k = 1`, 6 with `top_p = 0.3`, 2 with `min_p = 0.5`, 2 with `top_k = 20, top_p = 0.8`, and none with `top_k = 40, top_p = 0.95` (at temperature 0.7 or 1.0), `top_k = 5`, `top_p = 0.8`, `min_p = 0.1` or no narrowing. With the half budget for narrower requests no honest answer is rejected in any of them. A lower temperature sharpens the distribution before the `top_p` mask, so `top_p` below 1 under a temperature below 0.7 also counts as narrower: with `top_p = 0.95` at temperature 0.05 the support is mostly the argmax alone, and on the MiniMax-M2.7 honest pairs (processed logprobs rebuilt from each side's top 5) the 3 % budget rejects outputs at temperatures 0.05 and 0.1 and none at 0.7. Making the check log-only for every narrowing request instead would switch it off for every corpus request (DeepSeek processed catch 3.3 → 2.6 % NVFP4, 84.4 → 80.7 % old checkpoint) and for a fabricated output under a collapsed sampler.

## Validation scope and known limitations

Inference validation in devshard is an interim mechanism and needs a careful redesign. It compares logprobs from two GPUs that never agree exactly, so it is a statistical test: every threshold trades honest rejections against missed substitutions, and the trade is set in favour of serving. An honest executor must not be rejected because of hardware drift, sampler settings or output length, since a false reject counts against an honest host and fails a request a user paid for. The constants above are set at zero measured honest rejection on the calibrated models, and a check is enforced only where calibration data supports it.

The current design does not cover every edge case, and this is deliberate. Validation can only decide what the validator can reproduce, and several request shapes and conditions sit outside that: sampler settings that differ from the calibration sampler (`top_k`, `top_p`, `min_p`, `temperature`, their combinations and server-side defaults), models and output lengths without calibration data, hardware pairs that were not measured, and close substitutes whose logprobs stay within honest drift. For these inputs validation widens its tolerance or leaves the decision to a weaker check, so some substitutions pass with a measured or unmeasured probability. The list under "Assumptions and residual risk" names the cases measured so far. It is not exhaustive: another combination of request parameters, model or hardware that weakens a check is expected and belongs to the same known class.

These gaps are known technical debt of the validation mechanism, and the redesign is in progress. Its goal is a scheme that is secure by construction and stable under real serving conditions: validation in `raw_logprobs` (see the follow-up below), calibration across all live models and hardware pairs, a sampler-support check that does not depend on the request's sampler, and checks whose guarantees do not rest on per-model thresholds. Until then, changes in this area tighten individual checks and do not claim full coverage.

## Assumptions and residual risk

Model substitution on short outputs is still possible with some probability. This change closes the dilution hole; it does not make short outputs as well-verified as long ones.

- **Weak catch of close substitutes.** NVFP4 substitution on DeepSeek processed short outputs is caught about 4 % of the time (3.9 %), and 3.3 % over all lengths. Old-checkpoint substitution on short outputs is caught 44.5 %. A substitute this close to the genuine model mostly passes.
- **Three live models are uncalibrated.** MiniMax-M2.7, Kimi-K2.6 and GLM-5.2-FP8 have no honest+fraud corpora. They run the per-position ceiling and sampler-support checks log-only, so only the mean-distance check decides; their short-output honest-reject and catch rates are unmeasured. The mean-distance check is never more lenient than legacy at any length; the log-only column above shows what they get on the calibrated models (GLM-5.3 processed NVFP4 catch 72.7 % against legacy 72.6 %).
- **One hardware pair per model.** Honest scale differs by up to 5× across hardware pairs in the experiments' cross-hardware summary; other pairs may reject more honest outputs or catch fewer substitutes.
- **Substitution under a narrow sampler.** With the half budget the sampler-support check stops only fabrication; a substitute is left to the ceiling and mean-distance checks. With `top_k = 1` the processed logprobs carry the argmax alone, so those checks barely see a substitute: in the reproduction a 4-bit and a base-model substitute (about 21 % of positions out of support) were caught 0 of 100 under `top_k = 1` (99–100 under the 3 % budget, which also rejected 10 % of honest answers), and 18–22 of 100 under `min_p = 0.5`. A validator that recomputed the support one token wider (`top_k = 2`) caught 70 of 100 with no honest rejection, but that needs a second replay; validating narrow requests in `raw_logprobs` is the cleaner follow-up.
- **Server-side sampler defaults.** The validator reads the sampler from the request. A model whose `generation_config` narrows the sampler for requests that do not set it gets the 3 % budget.
- **Top-4 corpora.** The corpora keep only the top-4 logprobs, so a validator −9999 is visible only when the token is inside the validator's top-4. Production has the token's own logprob, so the sampler-support check will fire more often, for honest and fraud outputs alike, than measured.
- **No held-out split.** The allowance 0.10 and tolerance 0.02 were chosen on the same 1000-prompt sets they are evaluated on.
- **Thin GLM-5.3 short evidence.** GLM-5.3-Flash has only 3 honest outputs under 64 tokens; all short-slice evidence is DeepSeek's.
- **GLM-5.3 processed honest rejection stays at legacy's 0.6 %.** All six are outputs of 257+ tokens, where the check is the legacy score.
- **Request-path vocabulary lookups.** The first `stop_token_ids` request per (epoch, model) waits for a chain query and a Hugging Face fetch (10 s timeout); a failure is a 400 and is retried after 10 minutes.
- **No real-hardware run.** End-to-end coverage is in process (gateway session, three hosts, stub engine and validators, `TestShortOutputSession_*`); whether the engine honours `stop_token_ids` on real hardware is untested.

## Rollout

Ships as a new devshard version. versiond routes a session only to hosts running the same devshard version, so a session never mixes the old and new rules: existing sessions keep the 64-token floor and the legacy score on the old version, and sessions started on the new version use short-output validation throughout. No chain upgrade.

Before enforcing the output checks on another model, collect its calibration data and watch the `would fail (log-only)` lines, then add it to `OutputChecksEnforcedModels`.

## Follow-up: validate on `raw_logprobs`

With each mode given its own threshold at 1 % honest rejection, raw separates honest from substituted outputs better on every model and slice measured (legacy scorer, same footing), fraud caught in %:

| model | slice | processed: NVFP4 / old-ckpt caught | raw: NVFP4 / old-ckpt caught |
|---|---|---|---|
| DeepSeek-V4-Flash-0731 | all | 11.8 / 75.0 | 22.3 / 84.2 |
| DeepSeek-V4-Flash-0731 | N<64 | 2.2 / 28.2 | 8.1 / 50.9 |
| GLM-5.3-Flash | all | NVFP4 78.5 | NVFP4 99.7 |

Raw has lower honest variance across hardware, which is what allows the tighter threshold. Switching is a governance change: every live model needs a threshold re-derived for raw (data exists for two of five today), and the sampler-support check has no effect in raw mode. Short-output validation is correct in both modes, so the switch can follow on its own schedule.
