# Troubleshooting

Every parameter that is stripped / rejected / normalized at the gateway is documented here with rationale, edge-cases, and links to vendor docs. Anchors are stable — link directly from on-call channels / GitHub comments.

## Quick error map

| HTTP / behavior | Common cause | Anchor |
|-----------------|--------------|--------|
| 400 `"<param>" is currently rejected by the Gonka network` | non-allowlist parameter | [#reject-unknown-param](#reject-unknown-param) |
| 400 `...name must be a non-empty string` on a whitespace-only name | names are trimmed before the non-empty check | [#reject-whitespace-names](#reject-whitespace-names) |
| 400 `messages[N].tool_calls[M].id is duplicated` | Kimi-K2.6 model-side bug | [#reject-duplicate-tool-call-id](#reject-duplicate-tool-call-id) |
| 400 on `tags` | undocumented field | [#reject-tags](#reject-tags) |
| 400 on `guided_json` / `guided_regex` / etc. | vLLM-native structured decoding | [#reject-guided-decoding](#reject-guided-decoding) |
| 400 on `structured_outputs` with Kimi-K2.6 route | Moonshot API does not declare | [#reject-structured_outputs-kimi](#reject-structured_outputs-kimi) |
| 400 on `enforced_tokens` | no validator written | [#reject-enforced_tokens](#reject-enforced_tokens) |
| 400 on `allowed_token_ids` / `ignore_eos` / `use_beam_search` / `truncate_prompt_tokens` / `prompt_logprobs` | vLLM-native, safety/abuse risk | [#reject-vllm-internals](#reject-vllm-internals) |
| Silent disappearance of a param | silent-strip allowlist | search by param name under [#silent-strips](#silent-strips) |
| `thinking.type` value normalized | `adaptive` / `auto` resolved to `enabled` | see [Kimi overrides](kimi-k2.6.md#parameter-overrides) |
| `tool_choice: "required"` becomes `"auto"` | network policy | [#coerce-tool-choice-required](#coerce-tool-choice-required) |
| `n` becomes 1 at `temperature == 0` | vLLM constraint | [#coerce-n-when-temperature-zero](#coerce-n-when-temperature-zero) |
| `extra_body` keys appear at top level | OpenAI Python SDK passthrough | [#unwrap-extra_body](#unwrap-extra_body) |
| `enable_thinking` lifts into `chat_template_kwargs` | Qwen3 canonical placement | [#translate-enable_thinking](#translate-enable_thinking) |
| `reasoning` object decomposed to top-level `reasoning_effort` | OpenRouter unified-reasoning convention | [#translate-reasoning](#translate-reasoning) |
| 400 on out-of-range `top_p` / `repetition_penalty` / `top_k` | value outside backend-accepted range | [#reject-out-of-range-sampling](#reject-out-of-range-sampling) |
| 400 on `max_tokens: 0` (non-Kimi route) | zero output budget | [#reject-nonpositive-max-tokens](#reject-nonpositive-max-tokens) |
| 400 on wrong-typed param (bool / int / array element) | type mismatch caught at the gateway | [#reject-malformed-param-types](#reject-malformed-param-types) |
| `thinking_token_budget` forced to `0` on Kimi-K2.6 with small `max_tokens` | content-headroom guard | [#kimi-empty-content-think-burn](#kimi-empty-content-think-burn) |
| Empty `content` / `finish_reason=length` on Kimi-K2.6 | thinking ate the budget | [#kimi-empty-content-think-burn](#kimi-empty-content-think-burn) |

## Silent strips

### #strip-cache_key

**What**: `cache_key: "<value>"` is removed from the request body before forwarding.

**Why**: `cache_key` is a Moonshot Kimi native top-level context-cache hint documented for the Kimi Code Plan tier [[Moonshot-1]](references.md#moonshot). It is emitted in the wild by Moonshot's own `kimi-cli`, which forwards `cache_key: "kimi-cli_<hash>"` even when the target endpoint is not a Moonshot-hosted API. Our path serves the same Kimi-K2.6 weights via vLLM, which does not honor `cache_key` — vLLM uses a distinct `cache_salt` field for prompt-cache isolation [[vLLM-3]](references.md#vllm) [[vLLM-13]](references.md#vllm), and the open aliasing request [[vLLM-7]](references.md#vllm) remains unmerged. Forwarding the field bare would imply cache-isolation guarantees we cannot deliver in a domain with [published prompt-cache timing side-channel attacks](https://arxiv.org/abs/2502.07776).

**When to restore**: when multi-tenant cache isolation lands via hash → `cache_salt` injection; restore together with `prompt_cache_key` — both share the same upstream gap and should bridge as one feature.

**Fix (client-side)**: drop the field if not needed; the gateway provides no cache-key semantics today.

<details>
<summary>Sample request body fragment</summary>

```json
{
  "cache_key": "kimi-cli_f1c55293"
}
```

</details>

---

### #strip-prompt_cache_key

**What**: `prompt_cache_key: "<value>"` is removed from the request body before forwarding.

**Why**: `prompt_cache_key` is a first-class OpenAI Chat Completions field for prompt-cache routing and sharding hints [[OpenAI-1]](references.md#openai), and is also documented by Moonshot for the Kimi Code Plan tier [[Moonshot-1]](references.md#moonshot). The vLLM-served path does not honor it — vLLM uses `cache_salt` for prompt-cache isolation [[vLLM-3]](references.md#vllm) [[vLLM-13]](references.md#vllm), and a request to alias `prompt_cache_key` → `cache_salt` has been open since January 2026 with no merged PR [[vLLM-7]](references.md#vllm). Forwarding bare would give clients false cache-isolation guarantees in a domain with [published prompt-cache timing side-channel attacks](https://arxiv.org/abs/2502.07776).

**When to restore**: same trigger as `#strip-cache_key` — when a hash → `cache_salt` bridge lands; both fields share one rationale and should restore together.

**Fix (client-side)**: drop the field if not needed; no cache routing is performed on the gateway path.

---

### #strip-service_tier

**What**: `service_tier: "auto"|"default"|"flex"|"priority"` is removed from the request body before forwarding.

**Why**: `service_tier` is an OpenAI billing and latency tier routing field [[OpenAI-2]](references.md#openai) [[OpenAI-3]](references.md#openai) that selects a processing queue (flex for throughput, priority for low-latency). vLLM exposes a single queue and the field is absent from `ChatCompletionRequest` — unknown fields are silently dropped by `extra='allow'` [[vLLM-2]](references.md#vllm) [[vLLM-12]](references.md#vllm). Stripping at the gateway makes the no-op behaviour explicit and auditable rather than letting it vanish silently in the upstream.

**When to restore**: n/a — vLLM has no tier concept.

**Fix (client-side)**: drop the field; it has no effect on this path.

---

### #strip-store

**What**: `store: true|false` is removed from the request body before forwarding.

**Why**: `store` is the OpenAI Stored Completions opt-in for distillation and eval pipelines [[OpenAI-1]](references.md#openai). vLLM does not persist completions; forwarding the field would create a phantom retention expectation for GDPR and audit workflows without any backing behaviour.

**When to restore**: if the gateway implements its own completion store layer that actually honours the flag.

**Fix (client-side)**: drop the field; no retention guarantee is provided by the gateway.

---

### #strip-provider

**What**: `provider: {...}` (the OpenRouter cross-provider routing object) is removed from the request body before forwarding.

**Why**: the `provider` object (`order`, `only`, `ignore`, `quantizations`, ...) is an OpenRouter edge-only routing construct [[OpenRouter-3]](references.md#openrouter) [[OpenRouter-1]](references.md#openrouter) that selects among OpenRouter's backend fleet. The gateway routes to a single vLLM backend; the object carries no routing semantic on this path and would be ignored by vLLM's `extra='allow'` even if forwarded.

**When to restore**: n/a — gateway is single-backend; OpenRouter routing has no equivalent.

**Fix (client-side)**: drop the field; backend selection is fixed by the `model` field.

---

### #strip-plugins

**What**: `plugins: [{...}]` is removed from the request body before forwarding.

**Why**: `plugins` is an OpenRouter edge-only mechanism for invoking hosted tools such as `web` search and `file-parser` [[OpenRouter-2]](references.md#openrouter) [[OpenRouter-6]](references.md#openrouter). These plugins are executed at the OpenRouter edge layer; they are never passed to a downstream model. vLLM has no plugin execution path, so forwarding the array would silently imply capability the backend does not have.

**When to restore**: n/a — plugin execution is an edge concern with no vLLM equivalent.

**Fix (client-side)**: drop the field; implement any equivalent tool behaviour in the client or as a separate sidecar.

---

### #strip-extra_headers

**What**: `extra_headers: {...}` is removed from the request body if it appears there.

**Why**: `extra_headers` is an OpenAI Python SDK convention for HTTP-level header injection, documented alongside `extra_body` and `extra_query` in the SDK's "Undocumented request params" section [[OpenAI-5]](references.md#openai). Under correct SDK usage the field is applied at the HTTP transport layer and never serialised into the JSON body. A literal `extra_headers` key in the body indicates a client that accidentally serialised the SDK construct into the wire body rather than passing it to the HTTP layer. Header injection is an HTTP concern, not a body concern — there is no meaningful body-level semantic to honour.

**When to restore**: n/a — header injection belongs on the HTTP layer, not in the request body.

**Fix (client-side)**: pass `extra_headers` to the SDK's request options (where it writes HTTP headers), not into the body dict; if constructing raw HTTP, set headers directly on the request.

---

### #strip-thinking_config

**What**: `thinking_config: {...}` is removed from the request body before forwarding.

**Why**: `thinking_config` (Google's `thinkingConfig: {thinkingBudget, includeThoughts}`, camelCase, nested under `generationConfig`) is a Gemini-native reasoning-control shape. It does not appear in the OpenAI Chat Completions contract [[OpenAI-1]](references.md#openai), in the OpenRouter unified parameters [[OpenRouter-1]](references.md#openrouter), in vLLM's `ChatCompletionRequest` schema, or in the Moonshot Kimi API [[Moonshot-1]](references.md#moonshot). There is no mapping from this shape to any field the served models accept. Silent-strip is the lowest-friction option for clients that mistakenly forward a Gemini snippet to this endpoint.

**When to restore**: n/a — purely a different provider's convention with no equivalent on the vLLM path.

**Fix (client-side)**: drop the field; use `thinking: {"type": "enabled"}` (Kimi) or `enable_thinking: true` (Qwen) instead.

---

### #strip-think

**What**: `think: true|false` is removed from the request body before forwarding.

**Why**: `think` is an [Ollama-style top-level reasoning flag](https://ollama.com/blog/thinking) emitted by Cline and other Ollama-CLI-compatible clients that target multiple backends. No vLLM-served route on the gateway today is reasoning-capable, so silent-strip mirrors the treatment of `thinking_config` and validated-then-stripped `reasoning_effort`.

**When to restore**: when a reasoning-capable route is added — `think: true` should then be translated to the same sink as `enable_thinking` (Qwen) or `thinking` (Kimi).

**Fix (client-side)**: use `enable_thinking: true` (Qwen) or `thinking: {"type": "enabled"}` (Kimi) instead of the Ollama-specific flag.

---

### #strip-display-thinking-sibling

**What**: the `display` field inside the `thinking` wrapper object is removed; the outer `thinking` object itself is kept and processed normally.

**Why**: `display` (e.g. `"summarized"`) is a Claude Code CLI UI hint that controls how thinking output is rendered in the CLI surface. The [Anthropic extended thinking docs](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking) enumerate the `thinking.type` wire enum (`enabled`/`disabled`) [[Anthropic-1]](references.md#anthropic) but do not document a `display` sibling as a wire field. vLLM has no semantics for it — the field is a client-side presentation concern that should be resolved by the SDK before the HTTP call.

**When to restore**: n/a — `display` is a UI concern; it is never a wire concept for vLLM.

**Fix (client-side)**: the SDK should resolve `display` client-side before sending the HTTP request; if you observe it on the wire, your client is leaking UI state into the body.

<details>
<summary>Sample request body fragment</summary>

```json
{
  "thinking": {
    "type": "adaptive",
    "display": "summarized"
  }
}
```

</details>

### #strip-safety_identifier

**What**: `safety_identifier: "<value>"` removed from the request body for non-Kimi routes.

**Why**: OpenAI is migrating end-user attribution from `user` to `safety_identifier` ([OpenAI-6 — safety identifier help center]). The field is gateway-stripped on routes that don't consume it. On Kimi-K2.6 it's forwarded (Moonshot consumes for abuse tracking on their hosted backend) — see [Kimi override](kimi-k2.6.md#parameter-overrides). The strip on other routes is the OpenAI-compatible no-op; vLLM's `extra='allow'` schema does not consume the field.

**When to restore**: when a non-Kimi route adds documented abuse-tracking semantics.

**Fix (client-side)**: send `user` instead if you need OpenAI-compatible attribution; the gateway uniformly validates that field with a 512 B cap.

---

### #strip-thinking-minimax

**What**: top-level `thinking: {...}` (Anthropic-style wrapper) silent-stripped on the MiniMax-M2.7 route before `ThinkingValidator` runs.

**Why**: MiniMax-M2.7 has no `chat_template_kwargs` switch for thinking — interleaved reasoning is structural to the chat template and always-on. The model emits `<think>...</think>` blocks inline in `content` by design ([[MiniMax-2]](references.md#minimax)). Mirroring `thinking` into template kwargs (as on Kimi-K2.6) or normalizing it (as on Qwen-style routes) is a no-op on this route. Silent-strip is the closest behavior to the model's actual contract.

**When to restore**: when MiniMax adds a documented per-request thinking toggle.

**Fix (client-side)**: stop sending `thinking`; for clients that need to suppress the visible thinking display, parse + filter `<think>...</think>` blocks client-side from the response content.

---

### #strip-enable_thinking-minimax

**What**: top-level `enable_thinking: bool` silent-stripped on the MiniMax-M2.7 route before `EnableThinkingValidator` runs (which would otherwise translate it into `chat_template_kwargs.enable_thinking`).

**Why**: vLLM Issue [vLLM-25](references.md#vllm) ([#36778](https://github.com/vllm-project/vllm/issues/36778)) confirms `chat_template_kwargs.enable_thinking=false` does NOT disable thinking on M2.5+. Forwarding the translated kwarg would silently mislead clients into believing they'd disabled reasoning when in fact the model still emits `<think>` blocks. Strip avoids the misleading appearance of effect.

**When to restore**: when the upstream Issue is fixed and the kwarg is honored on the M2 line.

**Fix (client-side)**: stop sending `enable_thinking` on this route — see [strip-thinking-minimax](#strip-thinking-minimax) for the broader story.

---

### #strip-tool_call_id-minimax

**What**: `tool_call_id` on `role:"tool"` messages silent-stripped on the MiniMax-M2.7 route during message normalization.

**Why**: MiniMax-M2.7 tool messages correlate by `name` + positional order within a tool-result block, not by `tool_call_id` ([[MiniMax-4]](references.md#minimax)). Clients porting OpenAI code may dual-emit the field. Forwarding it is harmless (vLLM ignores) but rejecting it would force every OpenAI-compat client to fork their tool-message serializer per route. Silent-strip preserves compat without implying semantics we don't honor.

**When to restore**: never — the field has no consumer on this route.

**Fix (client-side)**: omit `tool_call_id` for cleanest payloads. If you must dual-emit (e.g. shared serializer across routes), it's a free pass-through to silent-strip.

---

## Validates-then-strips

### #strip-reasoning_effort

**What**: `reasoning_effort: "none"|"minimal"|"low"|"medium"|"high"|"xhigh"` enum-validated, then field stripped from the request body before forwarding to vLLM.

**Why**: vLLM declares the enum [[vLLM-1]](references.md#vllm) (sourced from [[OpenAI-4]](references.md#openai) reasoning guide; we exclude `"max"` because no routed model is DeepSeek). Both currently-routed models are non-reasoning — [[Qwen-1]](references.md#qwen) for Qwen3-235B-Instruct-2507, [[Moonshot-1]](references.md#moonshot) for Kimi (schema lacks the field). The validate-then-strip pattern surfaces malformed enum values as a 400 instead of silently forwarding garbage; the strip itself is the documented no-op on both backends.

**When to restore**: when a reasoning-capable model is added to the gateway routes — strip wiring must be revisited then.

**Fix (client-side)**: if you're sending `reasoning_effort` and need the behavior, you're on a route that doesn't support it. Either drop the field or wait for a reasoning-capable route to be added.

## Translations / coercions

### #translate-enable_thinking

**What**: top-level `enable_thinking: true|false` lifted into `chat_template_kwargs.enable_thinking`; original top-level key removed.

**Why**: canonical Qwen3 placement for `enable_thinking` is inside `chat_template_kwargs`, as documented in the Qwen vLLM deployment guide [[Qwen-3]](references.md#qwen) — "Passing enable_thinking is not OpenAI API compatible" at the top level. A pre-existing `chat_template_kwargs.enable_thinking` wins on conflict; the translation is skipped.

**When to restore**: n/a — this is permanent normalization. Lift remains valid as long as Qwen3 chat templates accept the kwarg.

**Fix (client-side)**: send `chat_template_kwargs.enable_thinking` directly to skip the translation step.

---

### #translate-reasoning

**What**: object `reasoning: {effort, max_tokens, exclude, enabled}` decomposed; `effort` lifted to top-level `reasoning_effort`; the wrapper object removed.

**Why**: OpenRouter's unified-reasoning-tokens convention uses the `reasoning` object with `effort`/`max_tokens`/`exclude`/`enabled` sub-fields [[OpenRouter-4]](references.md#openrouter). `enabled: false` is honored as an explicit opt-out — no lift occurs. `max_tokens`, `exclude`, and `enabled: true` are silent-dropped (no documented sink on non-reasoning routes). Top-level `reasoning_effort` wins on conflict.

**When to restore**: n/a — this is permanent normalization.

**Fix (client-side)**: send `reasoning_effort` directly; this skips both this translation and the subsequent `#strip-reasoning_effort` enum validation path.

---

### #coerce-tool-choice-required

**What**: `tool_choice: "required"` silently rewritten to `tool_choice: "auto"`.

**Why**: `"required"` is temporarily disabled by network policy due to historical cost-amplifier behavior and engine-wedge observations. Coercing to `"auto"` keeps OpenAI-spec-compatible clients working transparently — the OpenAI Chat Completions reference [[OpenAI-1]](references.md#openai) documents both `"auto"` and `"required"` as valid values.

**When to restore**: when network policy re-enables `"required"` — remove the coerce in `ToolsValidator.Validate`.

**Fix (client-side)**: if you need true `"required"` semantics, file a network request. The gateway currently provides best-effort `"auto"` instead.

---

### #coerce-n-when-temperature-zero

**What**: `n: <N>` coerced to `n: 1` whenever `temperature == 0`.

**Why**: vLLM rejects `n > 1` with `temperature == 0` — greedy sampling produces identical completions, so vLLM treats this as a malformed request (`Best of with temperature 0` error). Rather than returning a 400, the gateway silently rounds down to `n: 1`, matching the sole semantically valid value under deterministic sampling.

**When to restore**: when vLLM relaxes the constraint.

**Fix (client-side)**: either set `temperature > 0` (typical) or accept `n: 1` — deterministic sampling produces one output anyway.

---

### #unwrap-extra_body

**What**: `extra_body: {keyA: valueA, ...}` envelope opened; each inner key lifted to the top level of the request document; envelope removed.

**Why**: the OpenAI Python SDK convention is to flatten `extra_body` client-side into the JSON body before the HTTP call [[OpenAI-5]](references.md#openai) — a literal `extra_body` key on the wire indicates either a non-flattening client (e.g. some LiteLLM passthrough configs) or hand-written code that copied the SDK construct verbatim. The catalog pre-pass lifts inner keys before `rejectUnknownParameters` runs; lifted keys flow through normal validation. Top-level keys win on conflict. Nested `extra_body` inside `extra_body` is not re-lifted (no recursion). Non-object envelopes (`extra_body: "x"` / `null` / `[]` / `42`) are silently dropped.

**When to restore**: n/a — unwrap is the canonical SDK-compat behavior; no restore path needed.

**Fix (client-side)**: pre-flatten in your client (correct OpenAI SDK usage); or trust the unwrap.

## Hard rejects (HTTP 400)

### #reject-unknown-param

**What**: HTTP 400, `feature "<name>" is currently rejected by the Gonka network. Some non-standard parameters can crash the vLLM engine on Gonka Host MLNodes, so the network rejects parameters that are not explicitly supported (see: https://github.com/gonka-ai/gonka/blob/main/docs/chat-api/README.md). If you do not need this parameter, remove it from the request; if you need it, file a request at https://github.com/gonka-ai/gonka/issues`.

**Why**: Closed-allowlist policy at the gateway. vLLM's `extra='allow'` model can crash the engine when unknown fields hit certain code paths; the conservative gate keeps the network stable. See the [vLLM project](https://github.com/vllm-project/vllm) for the upstream `extra='allow'` behavior.

**When to restore**: n/a — policy-level decision.

**Fix (client-side)**: drop the unknown field from your request body; if you need it, file an issue at https://github.com/gonka-ai/gonka/issues.

---

### #reject-duplicate-tool-call-id

**What**: HTTP 400, `messages[N].tool_calls[M].id is duplicated`.

**Why**: The OpenAI Chat Completions spec requires each `tool_calls[].id` within an assistant message to be unique [[OpenAI-1]](references.md#openai). The same constraint is enforced by the Anthropic Messages API — Bedrock-served Claude returns `ValidationException: messages.N.content contain duplicate Ids: tooluse_...` (see [LiteLLM issue #15178](https://github.com/BerriAI/litellm/issues/15178)). The confirmed upstream source is a bug in vLLM's Kimi-K2.6 tool-call parser: `history_tool_call_cnt` is recomputed inside the per-choice loop with `n>1`, producing colliding `functions.<name>:<idx>` ids [[vLLM-14]](references.md#vllm). Captured-request evidence shows agents returning multiple distinct tool results for the same duplicated id — silent gateway-side dedup or rename would therefore risk information loss by discarding one of the real outputs.

**When to restore**: when the upstream vLLM Kimi-K2 parser fixes the counter-collision bug AND the OpenAI spec relaxes its uniqueness requirement — neither is likely in the near term.

**Fix (client-side)**: rewrite `tool_call.id` values to the canonical `functions.<name>:<global_idx>` form per Moonshot's official guidance [[Moonshot-3]](references.md#moonshot), OR rewrite to fresh UUIDs per the [OpenAI community workaround](https://community.openai.com/t/chatgpt-occasionally-reuses-tool-ids-in-the-same-session/577207). Do NOT deduplicate by ID lookup — both calls may have produced real distinct results.

<details>
<summary>Sample request body fragment (duplicate ids)</summary>

```json
{
  "role": "assistant",
  "tool_calls": [
    {"id": "functions.X:2", "function": {"arguments": "..."}},
    {"id": "functions.X:2", "function": {"arguments": "..."}}
  ]
}
```

</details>

---

### #reject-tags

**What**: HTTP 400 (unknown-param error — `tags` is not in the gateway allowlist).

**Why**: `tags` is a folk convention with no presence in any served chat-completions contract. The [Hermes Agent docs](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/api-server.md) describe a "standard OpenAI Chat Completions format" with no mention of `tags`. OpenRouter uses the structured `metadata` object for provider-level tagging [[OpenRouter-5]](references.md#openrouter). Codifying an undocumented field would mean documenting a contract with no vendor reference to back it.

**When to restore**: if/when a major served provider adds `tags` to their public API contract.

**Fix (client-side)**: use `metadata` for OpenAI-style tagging, or the `user` field for end-user tracking — both are accepted by the gateway.

---

### #reject-guided-decoding

**What**: HTTP 400 on any of `guided_json`, `guided_regex`, `guided_grammar`, `guided_choice`.

**Why**: These are vLLM-native structured-output fields superseded by the `structured_outputs` envelope [[vLLM-18]](references.md#vllm). Accepting them would bypass the `response_format` xgrammar bounds enforced to mitigate CVE-2025-48944 [[CVE-2]](references.md#security-advisories); the guided-decoding fields share the same grammar-compiler attack surface and would need their own equivalent validators before they could be shipped safely.

**When to restore**: if dedicated validators with bounds equivalent to the `response_format` / `structured_outputs` validators are written.

**Fix (client-side)**: use `response_format` with `type: "json_schema"` for the same structured-output intent — see the OpenAI Chat Completions reference [[OpenAI-1]](references.md#openai) for the schema.

---

### #reject-enforced_tokens

**What**: HTTP 400 on `enforced_tokens`.

**Why**: `enforced_tokens` is a vLLM-native field for forcing specific token ids during generation. No validator has been written; there is no observed client demand. Without a validator the field could be used to skip generation entirely (security and abuse concern), so the conservative position is to reject until bounds are defined.

**When to restore**: if a validator is written with bounds (max token count, blacklist of sensitive token ids).

**Fix (client-side)**: use `response_format`, `structured_outputs`, or system-prompt instructions for output control instead.

---

### #reject-vllm-internals

**What**: HTTP 400 on any of `allowed_token_ids`, `ignore_eos`, `use_beam_search`, `truncate_prompt_tokens`, `prompt_logprobs`.

**Why**: These vLLM-native fields either pose safety or abuse risks or expose internal generation state: `allowed_token_ids` can constrain the output vocabulary in ways that bypass safety layers; `ignore_eos` lets a client request unbounded generation; `use_beam_search` is deprecated upstream; `truncate_prompt_tokens` could manipulate billing or quota accounting; `prompt_logprobs` leaks internal token-probability state. Conservative rejection is safer than partial support for any of these.

**When to restore**: if a specific use case justifies one of these fields AND a validator with appropriate bounds is written.

**Fix (client-side)**: drop these fields; the gateway will not honor them.

---

### #reject-structured_outputs-with-response_format

**What**: HTTP 400 on requests that set BOTH `structured_outputs` and `response_format` — error message `structured_outputs: cannot be combined with response_format`.

**Why**: vLLM 0.20.0 merges `response_format` into `structured_outputs` via `dataclasses.replace()` ([vllm/entrypoints/openai/chat_completion/protocol.py:455-487](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/chat_completion/protocol.py)). The merged dataclass then trips `StructuredOutputsParams.__post_init__`'s exactly-one rule and surfaces as a 400 with a leaky pydantic dump that exposes private internal fields (`_backend`, `_backend_was_auto`, `disable_fallback`). Gateway 400 pre-empts the broker round-trip (no node lock, no quota burn) and returns a clean targeted error. Forward-compat: contract stays stable if vLLM changes merge semantics in a future release.

**When to restore**: never as-is — the conflict is fundamental to vLLM's merge logic. If vLLM ever defines explicit precedence and exposes it as a documented field, the gateway could honor it instead of rejecting.

**Fix (client-side)**: send only one of the two. `response_format` is the OpenAI-standard route for JSON / json_schema outputs; `structured_outputs` is the vLLM-extension route for regex / grammar / choice / structural_tag. If you need both styles, pick the one the rest of your client toolchain understands.

---

### #reject-structured_outputs-kimi

**What**: HTTP 400 on `structured_outputs` when the route resolves to `moonshotai/Kimi-K2.6`. (Other routes accept `structured_outputs` normally.)

**Why**: Per-route gate inside `StructuredOutputsValidator`. The Moonshot Kimi Chat Completion API [[Moonshot-1]](references.md#moonshot) does not declare `structured_outputs` in its schema; forwarding the field to the vLLM Kimi-K2 path can crash the engine. The validator's `RejectedModels` list includes Kimi-K2.6 explicitly. Other routes (e.g. Qwen3-Instruct) accept `structured_outputs` via the standard xgrammar path.

**When to restore**: when Moonshot declares `structured_outputs` (or an equivalent) in their Kimi API contract.

**Fix (client-side)**: use `response_format` with `type: "json_schema"` for Kimi-K2.6 (xgrammar-based, supported on this route); use `structured_outputs` only for non-Kimi routes.

### #accept-structured_outputs-minimax

**What**: `structured_outputs` is accepted on the `MiniMaxAI/MiniMax-M2.7` route (not rejected). Only Kimi-K2.6 rejects the field.

**Why**: The vLLM structured-output manager actively enforces the constraint on this route — verified end-to-end with paired discriminating/control requests: the `json`, `regex`, `choice`, `grammar`, and `json_object` kinds each steered output away from the natural answer the control produced. The engine holds the constraint, so there is no need to gate the field at the gateway.

**Caveat — `structural_tag` must be the OBJECT form**: a JSON-encoded *string* `structural_tag` crashes vLLM with an HTTP 500 on the request handler, so the gateway rejects the string form with a 400 on every route. Send the object shape (`{"type":"structural_tag","structures":[...],"triggers":[...]}`).

**Caveat — `response_format` conflict**: `structured_outputs` still cannot be combined with `response_format` (see [#reject-structured_outputs-with-response_format](#reject-structured_outputs-with-response_format)).

---

### #reject-whitespace-names

**What**: HTTP 400 on a name field that is present but contains only whitespace — e.g. `tools[].function.name`, `tool_choice.function.name`, `response_format.json_schema.name`, or a MiniMax tool-result entry `name`. The error reads `...name must be a non-empty string`.

**Why**: Every name validator trims with `strings.TrimSpace` *before* its non-empty check. Without the trim a whitespace-only name passes the gateway, reaches the engine as an effectively empty / nonexistent tool or schema name, and the request produces no usable output — it hangs until the deadline (0 bytes) and ties up a request slot/escrow. This is the same hang class as `max_tokens: 0` and `n: 0`. The engine is not crashed; the request just never completes.

**Maintainer note**: when adding any new name/identifier field to a validator, check it with `strings.TrimSpace(value) == ""` (or `messagevalidators.RequiredNonEmptyString`), never a bare `== ""` — a bare empty-string check is a whitespace-bypass that re-opens this hang.

**Fix (client-side)**: send a real (non-blank) name.

---

### #accept-tool-message-minimax-shape

**What**: On the MiniMaxAI/MiniMax-M2.7 route, `role:"tool"` messages MUST carry `content` as an array of `{name, type:"text", text}` objects (the MiniMax-native shape per [[MiniMax-4]](references.md#minimax)) — not the OpenAI string + `tool_call_id` shape. Bare-string content is rejected with HTTP 400.

**Why**: MiniMax's tool-calling contract correlates tool results by per-entry `name` + positional order inside a tool-result block; there is no `tool_call_id`. The MinimaxToolMessage content validator (registered in the per-model role policy override) enforces the entry shape and caps (≤16 entries, name ≤64 B, text ≤64 KiB, closed allow-list of keys to defend against [[SGLang-2]](references.md#sglang) union-with-null crash class). `tool_call_id`, if dual-emitted by a client porting from OpenAI, is silently stripped — see [strip-tool_call_id-minimax](#strip-tool_call_id-minimax).

**When to restore**: when MiniMax adds OpenAI-compat tool-message handling to their parser.

**Fix (client-side)**: emit the M2.7 shape:
```json
{"role": "tool",
 "content": [
    {"name": "<function_name>", "type": "text", "text": "<json-stringified result>"}
 ]}
```
Multiple parallel tool results in one block: one array entry per call.

---

### #reject-out-of-range-sampling

**What**: HTTP 400 on `top_p ≤ 0`, `repetition_penalty ≤ 0`, or a `top_k` that is neither `-1` nor `≥ 1`.

**Why**: These sampling knobs have ranges the backend enforces: `top_p` must be in `(0, 1]` and `repetition_penalty` must be `> 0` per the OpenAI/vLLM wire schema [[OpenAI-1]](references.md#openai), [[vLLM-1]](references.md#vllm); `top_k` accepts `-1` (disabled) or any integer `≥ 1`. The low end is an *exclusive* bound, so clamping to it would itself be invalid (e.g. `top_p = 0` is not a legal value) — the gateway returns a clear, field-named 400 at the boundary instead of forwarding a value the engine rejects with an opaque error. Values above the *upper* bound are clamped instead (`top_p > 1 → 1`, `repetition_penalty > 2 → 2`), and `temperature` / `min_p` are clamped into `[0, 2]` / `[0, 1]`.

**Fix (client-side)**: send `top_p` in `(0, 1]`, `repetition_penalty > 0`, and `top_k` as `-1` or a positive integer.

---

### #reject-nonpositive-max-tokens

**What**: HTTP 400 on `max_tokens: 0` (or `max_completion_tokens: 0`) for non-Kimi routes.

**Why**: A zero output budget is not a meaningful request — vLLM emits no content, and the gateway's redundancy layer then waits for a winner that can never arrive, so the request hangs to the deadline instead of failing fast. The gateway requires `max_tokens ≥ 1` [[OpenAI-1]](references.md#openai). Kimi-K2.6 instead floors small budgets to 16 as part of its think-burn mitigation (see [#kimi-empty-content-think-burn](#kimi-empty-content-think-burn)), so `0` becomes `16` on that route rather than a 400.

**Fix (client-side)**: send `max_tokens ≥ 1`, or omit it to take the route default.

---

### #reject-malformed-param-types

**What**: HTTP 400 when a parameter carries the wrong JSON type: non-boolean `stream` / `skip_special_tokens` / `detokenize` / `parallel_tool_calls`; non-integer `seed` / `min_tokens`; non-string elements in `stop` / `bad_words`; non-integer elements in `stop_token_ids`.

**Why**: vLLM rejects these with type errors at the engine boundary, producing opaque upstream 400s. The gateway type-checks them up front against the OpenAI/vLLM wire schema [[OpenAI-1]](references.md#openai), [[vLLM-1]](references.md#vllm) so the client gets an immediate, field-named error instead of a backend round-trip.

**Fix (client-side)**: send each field with its declared type — booleans for the flags, non-negative integers for `seed` / `min_tokens` and `stop_token_ids` elements, strings for `stop` / `bad_words` elements.

## Per-model behavior

### #kimi-empty-content-think-burn

**What**: on Kimi-K2.6, small `max_tokens` requests can return `finish_reason=length` with `content=null`. The gateway also reshapes inbound requests:
- `max_tokens` and `max_completion_tokens` are floored to **16** ([PR #1227](https://github.com/gonka-ai/gonka/pull/1227)).
- `thinking_token_budget` is resolved by a single validator with this precedence:
  1. `max_tokens < 256` → force `thinking_token_budget = 0` (bypass thinking entirely), overriding the client value.
  2. Otherwise, if `thinking_token_budget` is absent, default to `max_tokens / 2`.
  3. Cap at `96_000` (Moonshot HLE/AIME budget).
  4. Clamp to `max_tokens − 64` so visible content always has headroom after `</think>`.

**Why**: Kimi-K2.6 emits its reasoning inside `<think>...</think>`. vLLM's `kimi_k2` reasoning parser strips `</think>` as a special token; when the entire `max_tokens` budget is consumed by reasoning, no visible content reaches `delta.content` and the client sees an empty stream. This is the same documented outcome as OpenAI's reasoning models: *"the incomplete status might occur before any visible output tokens are produced, meaning you could incur costs for input and reasoning tokens without receiving a visible response"* — [[OpenAI-4]](references.md#openai). Anthropic documents the equivalent behavior for extended thinking with `stop_reason=max_tokens` ([[Anthropic-2]](references.md#anthropic)).

**Response-side**: when the empty stream arrives with `usage.completion_tokens > 0`, the gateway classifies the attempt as **model burn** rather than host fault — telemetry-only, no `failureStrikes` increment, no quarantine. Scoped to the Kimi-K2.6 route: `completion_tokens` is host-reported, so it is honored only where reasoning-burn is expected. This mirrors OpenAI/OpenRouter behavior where empty `finish_reason=length` is a valid passthrough response, not a provider failure.

**Fix (client-side)**:
- For chat / agent traffic: set `max_tokens ≥ 256` so the gateway leaves a default thinking budget. For long reasoning, set `max_tokens ≥ 4096` and consider `thinking_token_budget` explicitly.
- For probe / validation traffic: keep `max_tokens` small but expect `thinking_token_budget = 0` to be enforced.
- To opt out of thinking on a non-small `max_tokens`: send `thinking_token_budget: 0` **explicitly**. The validator preserves client-set zero. Note that `thinking:{type:"disabled"}` does NOT zero ttb — Kimi empirically ignores the disable hint on hard prompts ([PR #1202](https://github.com/gonka-ai/gonka/pull/1202)), so the validator keeps the `max_tokens / 2` safety net to prevent burn-through when the model thinks anyway.

---

## Per-model gotchas

Brief pointers to deeper notes in per-model docs:

- **Kimi-K2.6**: [Known model-side bugs we work around](kimi-k2.6.md#known-model-side-bugs-we-work-around)
- **Qwen3-235B-A22B-Instruct-2507**: [Known model-side bugs we work around](qwen3-235b-a22b-instruct-2507.md#known-model-side-bugs-we-work-around)
- **MiniMaxAI/MiniMax-M2.7**: [Known model-side bugs we work around](minimax-m2.7.md#known-model-side-bugs-we-work-around)
