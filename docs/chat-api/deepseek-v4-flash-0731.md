# DeepSeek-V4-Flash-0731 (`deepseek-ai/DeepSeek-V4-Flash-0731`) — overrides & extensions

Provider: DeepSeek. This doc documents how DeepSeek-V4-Flash-0731 deviates from the [universal contract](README.md). For params that behave the same as universal, see the universal contract directly.

Mirrors the structure of [Kimi-K2.6](kimi-k2.6.md), [Qwen3-235B](qwen3-235b-a22b-instruct-2507.md) and [MiniMax-M2.7](minimax-m2.7.md). This is the only routed model that consumes `reasoning_effort`, so the field's whole contract is documented here rather than in the universal doc.

## Model facts

| Property | Value | Source |
|----------|-------|--------|
| Provider | DeepSeek | [[DeepSeek-1]](references.md#deepseek) |
| vLLM route id | `deepseek-ai/DeepSeek-V4-Flash-0731` | — |
| Native thinking | yes — on by default when neither `thinking` nor `enable_thinking` is passed | [[vLLM-33]](references.md#vllm) |
| Reasoning effort levels | three: `low`, `high`, `max` | [[DeepSeek-1]](references.md#deepseek), [[vLLM-33]](references.md#vllm) |
| Chat rendering | dedicated vLLM tokenizer (`vllm/tokenizers/deepseek_v4.py`), not a Jinja `chat_template` | [[vLLM-33]](references.md#vllm) |
| Reasoning/tool parsing | dedicated vLLM parser (`vllm/parser/deepseek_v4.py`); the model card lists no `--reasoning-parser` flag | [[vLLM-34]](references.md#vllm), [[DeepSeek-1]](references.md#deepseek) |
| Engine version floor | vLLM 0.22.0 — see [Deployment requirements](#deployment-requirements) | [[vLLM-35]](references.md#vllm), [[CVE-15]](references.md#security-advisories) |

## Deployment requirements

Infrastructure-level constraints that must hold BEFORE this route is served — enforced by vLLM engine configuration, NOT by the gateway:

- **vLLM ≥ 0.22.0.** Two independent reasons converge on the same floor. The `reasoning_effort` → `enable_thinking` mapping ships in the v0.22.0 milestone ([[vLLM-35]](references.md#vllm)), and [[CVE-15]](references.md#security-advisories) (CVSS 9.1, OpenAI-API authentication bypass, affects 0.3.0 through 0.21.0) is fixed in the same release. A host old enough to ignore `reasoning_effort` is old enough to serve unauthenticated inference.
- **`--trust-remote-code` is required by the model card** ([[DeepSeek-1]](references.md#deepseek)), which puts the deployment inside the blast radius of [[CVE-12]](references.md#security-advisories). Mitigation is the same as every other route: pin the HuggingFace `revision=<commit-sha>`, never serve `revision=main`.
- **Effort levels are rendered by the engine, not the weights.** The value is consumed by vLLM's bundled DeepSeek-V4 tokenizer, so two hosts on different vLLM builds can render the same request into different prompts. This is a validation hazard specific to devshard: a verifier replays the inference and compares, so a mixed-build group disagrees on work nobody did wrong. Track it through the per-model capability machinery, not per-request.

## The `reasoning_effort` contract

The wire enum vLLM accepts is `Literal["none", "minimal", "low", "medium", "high", "xhigh", "max"]`, and its own field description states that `max` is DeepSeek-V4-specific and not part of the OpenAI spec ([[vLLM-1]](references.md#vllm)). A value outside that set is rejected by vLLM's own request validation, so the gateway's enum check is a fail-fast duplicate rather than the only guard.

`build_chat_params` forwards the value itself into `chat_template_kwargs` **and** derives `enable_thinking` from it, unless the caller set `enable_thinking` explicitly ([[vLLM-1]](references.md#vllm)):

```python
extra_kwargs = dict(..., reasoning_effort=self.reasoning_effort)
if self.reasoning_effort is not None and "enable_thinking" not in user_kwargs:
    extra_kwargs["enable_thinking"] = self.reasoning_effort != "none"
```

That forwarding is what makes the top-level field sufficient. Unlike Kimi's `thinking`, this route needs no mirroring into `chat_template_kwargs` by the gateway.

**Seven accepted values collapse to three rendered ones** ([[vLLM-33]](references.md#vllm)):

| Wire value | Rendered effort | Thinking | Prompt prefix at message index 0 |
|---|---|---|---|
| `none` | dropped | **off** (`thinking_mode="chat"`) | none — thinking mode is off |
| `minimal` | `low` | on | **empty string** |
| `low` | `low` | on | **empty string** |
| `medium` | `low` | on | **empty string** |
| `high` | `high` | on | "Reasoning Effort: Absolute maximum with no shortcuts permitted…" |
| `xhigh` | `high` | on | same as `high` |
| `max` | `max` | on | "Reasoning Effort: Beyond maximum — exhaustive, relentless and uncompromising…" |
| *absent* | `high` | on (default when neither `thinking` nor `enable_thinking` is passed) | same as `high` |

The levels are prompt prefixes, not sampling knobs: `REASONING_EFFORT_PROMPTS` maps each to a literal instruction block, injected once at message index 0 rather than per turn ([[vLLM-39]](references.md#vllm)). `low` maps to the empty string, so it adds no instruction at all.

The gateway fills the field on this route (see [Gateway wiring](#gateway-wiring)), so the *absent* row above describes the engine's own fallback rather than anything a host receives from us.

**Sending `minimal`, `low` or `medium` asks for less reasoning than sending nothing.** Those three resolve to `low` and inject no instruction, while an omitted field arrives as `max`. A client porting an OpenAI request that habitually carries `reasoning_effort: "medium"` therefore lands two levels below what it would have got by omitting the field. `xhigh` is an alias of `high` — accepted because vLLM accepts it, but it buys nothing over `high` and is billed the same.

The tokenizer's final `else` branch renders any unrecognized string as `high` rather than erroring, so the enum check upstream is what stops a typo from selecting a level the caller did not ask for.

## Parameter overrides

*Delta from [universal contract](README.md#supported-parameters-universal-behavior).*

| Param | Universal | On DeepSeek-V4-Flash-0731 | Why |
|-------|-----------|---------------------------|-----|
| `reasoning_effort` | enum-validated then **stripped on every route** | **forward** — the only route that consumes it | [[vLLM-33]](references.md#vllm), [[vLLM-1]](references.md#vllm) |
| `thinking` / `enable_thinking` | mirrored to `chat_template_kwargs` on Kimi; stripped on MiniMax | both read from `chat_template_kwargs`; default **on** when neither is present | [[vLLM-33]](references.md#vllm) |
| `thinking_token_budget` | injected/clamped on Kimi; stripped on Qwen and MiniMax | no equivalent knob in this tokenizer — the effort level is the budget control | [[vLLM-33]](references.md#vllm) |

## Native extensions

*Params unique to this route — no equivalent in the universal contract.*

| Param | Type | Behavior | Source |
|-------|------|----------|--------|
| `include_reasoning` | bool, default `true` | **Rejected by the top-level allowlist.** Distinct from `reasoning_effort=none`: that suppresses the thinking, this suppresses only its *delivery*, and it carries two effects rather than one. The parser nulls `delta.reasoning` and drops deltas that carried nothing else ([[vLLM-37]](references.md#vllm)), and serving marks reasoning as already ended, so structured-output grammar engages from the first token instead of after `</think>` ([[vLLM-38]](references.md#vllm)) — the thinking-plus-guided-decoding surface with known upstream bugs ([[vLLM-5]](references.md#vllm), [[vLLM-8]](references.md#vllm), [[vLLM-9]](references.md#vllm)). Generation is untouched either way, so the reasoning tokens are still billed through `usage.completion_tokens` while the client never receives them — the hidden-token class documented for `return_token_ids` ([[vLLM-19]](references.md#vllm)). Suppressing display is a client-side concern; suppressing billing is not something this field does. | [[vLLM-1]](references.md#vllm) |
| `chat_template_kwargs.drop_thinking` | bool, default `true` | Drops prior assistant thinking from rendered history. Passes through the `chat_template_kwargs` bounds/forbidden-key filter unchanged. The default discards prior thinking, the inverse of the round-trip contract MiniMax-M2.7 requires — a client porting between the two routes cannot carry its history handling across. | [[vLLM-33]](references.md#vllm) |

## Response shape

vLLM's canonical response field is `reasoning`; `reasoning_content` is the deprecated alias, renamed forward on input ([[vLLM-1]](references.md#vllm), [[vLLM-36]](references.md#vllm)). The gateway's stream reader accepts both names on `delta` and `message`, so either spelling reaches the client intact.

## Known model-side bugs we work around

- **Empty-thinking history collapse** ([[DeepSeek-2]](references.md#deepseek)): community reports on the model's own discussion thread describe reasoning loops in long tool-calling sessions, attributed to accumulating empty `<think></think>` blocks in history, with degradation reported after roughly 60 such turns. Reported remedy is `{"thinking": true, "reasoning_effort": "max"}`. Community hypothesis, not a vendor statement — no DeepSeek author replies in the thread. One claim in it is contradicted by the encoder: the reporters describe `low` *and* `high` as rendering empty reasoning prefixes, but `REASONING_EFFORT_PROMPTS` gives `high` a full instruction block and only `low` the empty string ([[vLLM-39]](references.md#vllm)). Since both `thinking` and the effort default to on/`high` already, the operative half of the reported remedy is the move from `high` to `max`, not the enabling of thinking. No gateway-side mitigation exists: only a client controlling its own history can avoid accumulating the empty blocks.

## Gateway wiring

`reasoning_effort` is enum-validated on every route and forwarded only here, via `ModelScopedParameterHandler{Models: []string{deepSeekV4Flash0731ModelID}}` in `defaultVLLMParameterCatalog`. The validator's allowed set is the vLLM wire enum including `max`. Nothing is mirrored into `chat_template_kwargs` — vLLM performs that merge itself, and doing it here would write the key twice.

**A request that omits the field is sent as `max`.** The engine's own fallback is `high`, the level the reasoning-loop reports name as degrading, so the route defaults to the strongest prefix the encoder defines rather than inheriting that fallback. The default only fills a gap: any explicit level the caller sends survives untouched, including levels weaker than the default. Because the levels are prompt prefixes, the stronger default lengthens generated reasoning, and those tokens are billed through `usage.completion_tokens` like any others — a caller wanting the shorter behavior sends `high` explicitly.

`reasoning: {"enabled": false}` is recorded as `reasoning_effort: "none"` rather than dropped. Dropping it would leave the request indistinguishable from one that never mentioned reasoning, which this default reads as permission to fill in — so a client disabling reasoning would have received maximum reasoning.

## See also
- [Troubleshooting](troubleshooting.md)
- [References](references.md)
- [Universal contract](README.md)
- [Kimi-K2.6 overrides](kimi-k2.6.md)
- [MiniMax-M2.7 overrides](minimax-m2.7.md)
