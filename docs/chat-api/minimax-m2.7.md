# MiniMax-M2.7 (`MiniMaxAI/MiniMax-M2.7`) — overrides & extensions

Provider: MiniMax AI. This doc documents how MiniMax-M2.7 deviates from the [universal contract](README.md). For params that behave the same as universal, see the universal contract directly.

Mirrors the structure of [Kimi-K2.6](kimi-k2.6.md) and [Qwen3-235B](qwen3-235b-a22b-instruct-2507.md). Chain-side wiring (HfCommit pin, ModelArgs, PoC params) lives in `inference-chain/app/upgrades/v0_2_13/upgrades.go`.

## Model facts

| Property | Value | Source |
|----------|-------|--------|
| Provider | MiniMax AI | [[MiniMax-1]](references.md#minimax) |
| vLLM route id | `MiniMaxAI/MiniMax-M2.7` | — |
| Total params | 229 B (sparse MoE; ~10 B activated, inherited from M2 base) | [[MiniMax-2]](references.md#minimax) |
| Tensor types | F32 / BF16 / F8_E4M3 (FP8) | [[MiniMax-2]](references.md#minimax) |
| Max context per sequence | 196 K tokens (180 K served) | [[MiniMax-3]](references.md#minimax) |
| Tool-call parser (vLLM) | `minimax_m2` | [[MiniMax-3]](references.md#minimax), [[vLLM-21]](references.md#vllm) |
| Reasoning parser (vLLM) | `minimax_m2_append_think` (per MiniMax guide); see [parser caveat](#deployment-requirements) | [[MiniMax-3]](references.md#minimax) |
| Native thinking | yes — **interleaved** `<think>...</think>` tags embedded in `content` | [[MiniMax-2]](references.md#minimax), [[MiniMax-4]](references.md#minimax) |
| Recommended sampling | `temperature=1.0, top_p=0.95, top_k=40` | [[MiniMax-2]](references.md#minimax) |

## Deployment requirements

Infrastructure-level constraints that must hold BEFORE this route is served — these are enforced by vLLM engine configuration / flags, NOT by the gateway:

- **HfCommit pinned** — `--trust-remote-code` is required by the model card ([[MiniMax-3]](references.md#minimax)), which puts the deployment inside the blast radius of [[CVE-12]](references.md#security-advisories) (vLLM hardcoded trust_remote_code bypass via malicious model repositories). Mitigation: pin the HuggingFace `revision=<commit-sha>` to a verified weights checkpoint. Never serve `revision=main`.
- **Tool-call parser MUST be `minimax_m2`** ([[vLLM-21]](references.md#vllm)). Drives the wire-format the model emits (`<minimax:tool_call>` XML, converted to OpenAI `tool_calls[]` on the response by vLLM).
- **Reasoning parser caveat** — official MiniMax recommendation is `--reasoning-parser minimax_m2_append_think` ([[MiniMax-3]](references.md#minimax)). vLLM upstream has known bugs on M2.5+ with this parser ([[vLLM-23]](references.md#vllm), [[vLLM-24]](references.md#vllm), [[vLLM-27]](references.md#vllm)): `extract_reasoning_streaming` assumes no opening `<think>` tag (M2.5+ emits one), and reasoning can be missing from SSE deltas. The visible-content stream still includes the reasoning, just not separated into `reasoning_content`. For the gateway this means `<think>...</think>` blocks land **inline in `content`** on responses — the pass-through design already handles this.
- **Pure TP8 is NOT supported** — vLLM recipe forbids it; H100 TP4+EP4 is the supported topology ([[vLLM-21]](references.md#vllm)).
- **AMD path (MI300X/MI325X/MI350X/MI355X)** requires `VLLM_ROCM_USE_AITER=1 VLLM_ROCM_SHUFFLE_KV_CACHE_LAYOUT=1`; first AITER launch JITs for several minutes ([[vLLM-21]](references.md#vllm)).
- **Avoid Ampere (A100) FP8** — M2.7 FP8 loads on vLLM v0.19.0 but crashes on nightly ([[vLLM-22]](references.md#vllm)). Either stay on a pinned tagged release or use BF16 on Ampere.
- **Disable `pythonic` and `qwen3_coder` tool-call parsers** — same vendor-agnostic CVE history that applies to Kimi/Qwen routes ([[CVE-1]](references.md#security-advisories), [[CVE-7]](references.md#security-advisories)).

## Parameter overrides

*Delta from [universal contract](README.md#supported-parameters-universal-behavior). Listed params behave differently on this route; everything else matches universal.*

| Param | Universal | On MiniMax-M2.7 | Why |
|-------|-----------|-----------------|-----|
| `tools[].function.strict` | (silent-strip in universal via `ToolsValidator`) | silent-strip — vLLM `minimax_m2` parser ignores | [[vLLM-21]](references.md#vllm) |
| `thinking` (top-level Anthropic-style object) | mirrored to `chat_template_kwargs.thinking` on Kimi route | **silent-strip on this route** — MiniMax has no `chat_template_kwargs` toggle for thinking; thinking is always on and emitted inline as `<think>...</think>` in `content`. Clients wanting to suppress thinking display must parse + filter client-side. | [[MiniMax-2]](references.md#minimax), [why](troubleshooting.md#strip-thinking-minimax) |
| `thinking_token_budget` | injected/clamped on Kimi; silent-strip on Qwen | **silent-strip** — no equivalent knob in MiniMax chat template | [[MiniMax-2]](references.md#minimax) |
| `enable_thinking` (top-level) | translated to `chat_template_kwargs.enable_thinking` on Qwen | **silent-strip on this route** — MiniMax does not honor; thinking is structural to the chat template, not configurable per request ([[vLLM-25]](references.md#vllm)). | [why](troubleshooting.md#strip-enable_thinking-minimax) |
| `safety_identifier` | strip (universal); pass-through on Kimi | strip (no MiniMax abuse-tracking API contract) | [[MiniMax-1]](references.md#minimax) |
| Content of `role:"tool"` messages | universal: string or text-part array, flattened to string; `tool_call_id` required | **MiniMax shape**: `content: [{name, type:"text", text}]` array; no `tool_call_id` (silently stripped if dual-emitted). Per-entry caps: ≤16 entries × `name` ≤64 B × `text` ≤64 KiB; closed allow-list of keys. | [[MiniMax-4]](references.md#minimax), [why](troubleshooting.md#accept-tool-message-minimax-shape) |

## Native extensions

*Params unique to this route — no equivalent in the universal contract.*

| Param | Type | Behavior | Source |
|-------|------|----------|--------|
| `extra_body.reasoning_split` | bool | If `true`, vLLM/MiniMax emit reasoning as a separate `reasoning_details[]` array on the response instead of inline `<think>...</think>` blocks in `content`. Gateway unwraps `extra_body` per universal contract; the lifted key passes through to vLLM. Document client responsibility: round-trip `reasoning_details` (or `<think>` blocks) verbatim into history. | [[MiniMax-5]](references.md#minimax) |
| `messages[].reasoning_details` | array | Assistant-turn reasoning history when client uses `reasoning_split=true`. **Pass-through** — assistant-side validator does not strip. **Critical:** stripping these breaks M2.7 interleaved-thinking continuity. | [[MiniMax-5]](references.md#minimax) |
| `<think>...</think>` blocks in assistant `content` | inline tags | **Pass-through verbatim** when round-tripped in history. The gateway message validator MUST NOT strip these from prior assistant turns or rewrite their internals. | [[MiniMax-2]](references.md#minimax), [[MiniMax-5]](references.md#minimax) |
| Tool-call output XML (`<minimax:tool_call><invoke name=…><parameter name=…>`) | tag stream | Emitted by the model verbatim; the vLLM `minimax_m2` tool-call parser converts to OpenAI `tool_calls[]` format on the response. Gateway pass-through — no special handling required on the response path. | [[MiniMax-4]](references.md#minimax), [[vLLM-21]](references.md#vllm) |
| MiniMax-only response fields (`base_resp`, `output_sensitive`, `input_sensitive`) | object / bool | **Pass-through** on the response. Document existence; clients may ignore. | [[MiniMax-5]](references.md#minimax) |

## Rejected MiniMax-platform-only keys

*Fields the MiniMax direct API (platform.minimax.io) accepts that the gateway rejects with HTTP 400 via the closed top-level allowlist. Listed so clients porting from the MiniMax platform see why their request fails.*

| Param | Why rejected |
|-------|--------------|
| `partial` | Assistant-prefill / continuation flag on the MiniMax platform. Rejected because it bypasses the gateway's per-role message validation (which treats the trailing turn as a new prompt, not a forced model continuation) and would expose vLLM to a chat-template integrity hazard that the OpenAI Chat Completions contract does not anticipate. Clients wanting equivalent behavior must shape the request through the normal `messages[]` array. [[MiniMax-5]](references.md#minimax) |
| `web_search` / `enable_search` / `search_kwargs` | MiniMax platform-side network-search features; vLLM does not implement them. Silent-stripping would mislead clients into thinking search ran. Fail-loud is the safer default. [[MiniMax-1]](references.md#minimax) |
| `mask_sensitive_info` | MiniMax platform-side content masking; not implemented in vLLM. Same fail-loud rationale. [[MiniMax-1]](references.md#minimax) |

## Structured outputs

| Field | Status | Note |
|-------|--------|------|
| `response_format` | ✅ supported (see universal) | xgrammar via vLLM; full schema bounds enforced. Compatible with thinking models since xgrammar runs on the `content`-emission phase, after thinking. |
| `structured_outputs` | ✅ **accepted on this route** | vLLM enforces the constraint on M2.7 (verified with discriminating/control requests across `json`/`regex`/`choice`/`grammar`/`json_object`). `structural_tag` must be the object form — the JSON-encoded string form is rejected (crashes the engine). See [accept-structured_outputs-minimax](troubleshooting.md#accept-structured_outputs-minimax). |

## Known model-side bugs we work around

- **Streaming tool_calls malformation** ([[vLLM-26]](references.md#vllm), [[SGLang-1]](references.md#sglang)): under SSE streaming a single tool call is sometimes emitted as two `tool_calls[]` entries — one with `name=null` and duplicated `arguments`. PR #35895 fixed the `stream_interval > 1` case; the `name=null` malformation may still occur with concurrent calls. Documented as a known client-responsibility issue.
- **Tool-call parser fails on `str | null` param types** ([[SGLang-2]](references.md#sglang)): `minimax_m2_tool_parser._convert_param_value` errors on union-with-null param values; clients should avoid `["string","null"]` in tool parameter schemas.
- **Reasoning missing in stream mode** ([[vLLM-27]](references.md#vllm)): when serving with `--reasoning-parser minimax_m2_append_think` and `stream=true`, reasoning_content is sometimes absent from delta chunks. Upstream bug; no gateway mitigation.
- **`thinking` cannot be disabled** ([[vLLM-25]](references.md#vllm)): even with `chat_template_kwargs.enable_thinking=false`, M2.5+ still emits `<think>` blocks. Confirms the strip-`enable_thinking` policy.
- **A100 / Ampere FP8 regression** ([[vLLM-22]](references.md#vllm)): M2.7 FP8 fails on Ampere with vLLM nightly. Mitigated by deployment-side version pin; no gateway responsibility.

## See also
- [Troubleshooting](troubleshooting.md)
- [References](references.md)
- [Universal contract](README.md)
- [Kimi-K2.6 overrides](kimi-k2.6.md)
- [Qwen3-235B overrides](qwen3-235b-a22b-instruct-2507.md)
