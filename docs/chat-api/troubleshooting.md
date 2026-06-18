# Troubleshooting

Every parameter that is stripped / rejected / normalized at the gateway is documented here with rationale, edge-cases, and links to vendor docs. Anchors are stable — link directly from on-call channels / GitHub comments.

## Quick error map

| HTTP / behavior | Common cause | Anchor |
|-----------------|--------------|--------|
| 400 `"<param>" is currently rejected by the Gonka network` | non-allowlist parameter | [#reject-unknown-param](#reject-unknown-param) |
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

## Silent strips

### #strip-cache_key

**What**: `cache_key: "<value>"` is removed from the request body before forwarding.

**Why**: `cache_key` is a Moonshot Kimi native top-level context-cache hint documented for the Kimi Code Plan tier [[Moonshot-1]](references.md#moonshot). It is emitted in the wild by Moonshot's own `kimi-cli`, which forwards `cache_key: "kimi-cli_<hash>"` even when the target endpoint is not a Moonshot-hosted API. Our path serves the same Kimi-K2.6 weights via vLLM, which does not honor `cache_key` — vLLM uses a distinct `cache_salt` field for prompt-cache isolation [[vLLM-3]](references.md#vllm) [[vLLM-13]](references.md#vllm), and the open aliasing request [[vLLM-7]](references.md#vllm) remains unmerged. Forwarding the field bare would imply cache-isolation guarantees we cannot deliver in a domain with [published prompt-cache timing side-channel attacks](https://arxiv.org/abs/2502.07776).

**When to restore**: when multi-tenant cache isolation lands via hash → `cache_salt` injection; restore together with `prompt_cache_key` — both share the same upstream gap and should bridge as one feature.

**Fix (client-side)**: drop the field if not needed; the gateway provides no cache-key semantics today.

**Captured-requests**: May 2026 batch — 159 captures from `kimi-cli` (e.g. `cache_key: "kimi-cli_f1c55293"`).

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

**Captured-requests**: n/a — no captures observed.

---

### #strip-service_tier

**What**: `service_tier: "auto"|"default"|"flex"|"priority"` is removed from the request body before forwarding.

**Why**: `service_tier` is an OpenAI billing and latency tier routing field [[OpenAI-2]](references.md#openai) [[OpenAI-3]](references.md#openai) that selects a processing queue (flex for throughput, priority for low-latency). vLLM exposes a single queue and the field is absent from `ChatCompletionRequest` — unknown fields are silently dropped by `extra='allow'` [[vLLM-2]](references.md#vllm) [[vLLM-12]](references.md#vllm). Stripping at the gateway makes the no-op behaviour explicit and auditable rather than letting it vanish silently in the upstream.

**When to restore**: n/a — vLLM has no tier concept.

**Fix (client-side)**: drop the field; it has no effect on this path.

**Captured-requests**: n/a — no captures observed.

---

### #strip-store

**What**: `store: true|false` is removed from the request body before forwarding.

**Why**: `store` is the OpenAI Stored Completions opt-in for distillation and eval pipelines [[OpenAI-1]](references.md#openai). vLLM does not persist completions; forwarding the field would create a phantom retention expectation for GDPR and audit workflows without any backing behaviour.

**When to restore**: if the gateway implements its own completion store layer that actually honours the flag.

**Fix (client-side)**: drop the field; no retention guarantee is provided by the gateway.

**Captured-requests**: n/a — no captures observed.

---

### #strip-provider

**What**: `provider: {...}` (the OpenRouter cross-provider routing object) is removed from the request body before forwarding.

**Why**: the `provider` object (`order`, `only`, `ignore`, `quantizations`, ...) is an OpenRouter edge-only routing construct [[OpenRouter-3]](references.md#openrouter) [[OpenRouter-1]](references.md#openrouter) that selects among OpenRouter's backend fleet. The gateway routes to a single vLLM backend; the object carries no routing semantic on this path and would be ignored by vLLM's `extra='allow'` even if forwarded.

**When to restore**: n/a — gateway is single-backend; OpenRouter routing has no equivalent.

**Fix (client-side)**: drop the field; backend selection is fixed by the `model` field.

**Captured-requests**: n/a — no captures observed.

---

### #strip-plugins

**What**: `plugins: [{...}]` is removed from the request body before forwarding.

**Why**: `plugins` is an OpenRouter edge-only mechanism for invoking hosted tools such as `web` search and `file-parser` [[OpenRouter-2]](references.md#openrouter) [[OpenRouter-6]](references.md#openrouter). These plugins are executed at the OpenRouter edge layer; they are never passed to a downstream model. vLLM has no plugin execution path, so forwarding the array would silently imply capability the backend does not have.

**When to restore**: n/a — plugin execution is an edge concern with no vLLM equivalent.

**Fix (client-side)**: drop the field; implement any equivalent tool behaviour in the client or as a separate sidecar.

**Captured-requests**: n/a — no captures observed.

---

### #strip-extra_headers

**What**: `extra_headers: {...}` is removed from the request body if it appears there.

**Why**: `extra_headers` is an OpenAI Python SDK convention for HTTP-level header injection, documented alongside `extra_body` and `extra_query` in the SDK's "Undocumented request params" section [[OpenAI-5]](references.md#openai). Under correct SDK usage the field is applied at the HTTP transport layer and never serialised into the JSON body. A literal `extra_headers` key in the body indicates a client that accidentally serialised the SDK construct into the wire body rather than passing it to the HTTP layer. Header injection is an HTTP concern, not a body concern — there is no meaningful body-level semantic to honour.

**When to restore**: n/a — header injection belongs on the HTTP layer, not in the request body.

**Fix (client-side)**: pass `extra_headers` to the SDK's request options (where it writes HTTP headers), not into the body dict; if constructing raw HTTP, set headers directly on the request.

**Captured-requests**: n/a — no captures observed.

---

### #strip-thinking_config

**What**: `thinking_config: {...}` is removed from the request body before forwarding.

**Why**: `thinking_config` (Google's `thinkingConfig: {thinkingBudget, includeThoughts}`, camelCase, nested under `generationConfig`) is a Gemini-native reasoning-control shape. It does not appear in the OpenAI Chat Completions contract [[OpenAI-1]](references.md#openai), in the OpenRouter unified parameters [[OpenRouter-1]](references.md#openrouter), in vLLM's `ChatCompletionRequest` schema, or in the Moonshot Kimi API [[Moonshot-1]](references.md#moonshot). There is no mapping from this shape to any field the served models accept. Silent-strip is the lowest-friction option for clients that mistakenly forward a Gemini snippet to this endpoint.

**When to restore**: n/a — purely a different provider's convention with no equivalent on the vLLM path.

**Fix (client-side)**: drop the field; use `thinking: {"type": "enabled"}` (Kimi) or `enable_thinking: true` (Qwen) instead.

**Captured-requests**: n/a — no captures observed.

---

### #strip-think

**What**: `think: true|false` is removed from the request body before forwarding.

**Why**: `think` is an [Ollama-style top-level reasoning flag](https://ollama.com/blog/thinking) emitted by Cline and other Ollama-CLI-compatible clients that target multiple backends. No vLLM-served route on the gateway today is reasoning-capable, so silent-strip mirrors the treatment of `thinking_config` and validated-then-stripped `reasoning_effort`.

**When to restore**: when a reasoning-capable route is added — `think: true` should then be translated to the same sink as `enable_thinking` (Qwen) or `thinking` (Kimi).

**Fix (client-side)**: use `enable_thinking: true` (Qwen) or `thinking: {"type": "enabled"}` (Kimi) instead of the Ollama-specific flag.

**Captured-requests**: n/a — no captures observed.

---

### #strip-display-thinking-sibling

**What**: the `display` field inside the `thinking` wrapper object is removed; the outer `thinking` object itself is kept and processed normally.

**Why**: `display` (e.g. `"summarized"`) is a Claude Code CLI UI hint that controls how thinking output is rendered in the CLI surface. The [Anthropic extended thinking docs](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking) enumerate the `thinking.type` wire enum (`enabled`/`disabled`) [[Anthropic-1]](references.md#anthropic) but do not document a `display` sibling as a wire field. vLLM has no semantics for it — the field is a client-side presentation concern that should be resolved by the SDK before the HTTP call.

**When to restore**: n/a — `display` is a UI concern; it is never a wire concept for vLLM.

**Fix (client-side)**: the SDK should resolve `display` client-side before sending the HTTP request; if you observe it on the wire, your client is leaking UI state into the body.

**Captured-requests**: May 2026 batch — observed in 5 captures with `{"thinking": {"display": "summarized", "type": "adaptive"}}`.

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

**Captured-requests**: n/a — no captures observed.

---

## Validates-then-strips

### #strip-reasoning_effort

**What**: `reasoning_effort: "none"|"minimal"|"low"|"medium"|"high"|"xhigh"` enum-validated, then field stripped from the request body before forwarding to vLLM.

**Why**: vLLM declares the enum [[vLLM-1]](references.md#vllm) (sourced from [[OpenAI-4]](references.md#openai) reasoning guide; we exclude `"max"` because no routed model is DeepSeek). Both currently-routed models are non-reasoning — [[Qwen-1]](references.md#qwen) for Qwen3-235B-Instruct-2507, [[Moonshot-1]](references.md#moonshot) for Kimi (schema lacks the field). The validate-then-strip pattern surfaces malformed enum values as a 400 instead of silently forwarding garbage; the strip itself is the documented no-op on both backends.

**When to restore**: when a reasoning-capable model is added to the gateway routes — strip wiring must be revisited then.

**Fix (client-side)**: if you're sending `reasoning_effort` and need the behavior, you're on a route that doesn't support it. Either drop the field or wait for a reasoning-capable route to be added.

**Captured-requests**: n/a — no captures observed.

## Translations / coercions

### #translate-enable_thinking

**What**: top-level `enable_thinking: true|false` lifted into `chat_template_kwargs.enable_thinking`; original top-level key removed.

**Why**: canonical Qwen3 placement for `enable_thinking` is inside `chat_template_kwargs`, as documented in the Qwen vLLM deployment guide [[Qwen-3]](references.md#qwen) — "Passing enable_thinking is not OpenAI API compatible" at the top level. A pre-existing `chat_template_kwargs.enable_thinking` wins on conflict; the translation is skipped.

**When to restore**: n/a — this is permanent normalization. Lift remains valid as long as Qwen3 chat templates accept the kwarg.

**Fix (client-side)**: send `chat_template_kwargs.enable_thinking` directly to skip the translation step.

**Captured-requests**: n/a — no captures observed.

---

### #translate-reasoning

**What**: object `reasoning: {effort, max_tokens, exclude, enabled}` decomposed; `effort` lifted to top-level `reasoning_effort`; the wrapper object removed.

**Why**: OpenRouter's unified-reasoning-tokens convention uses the `reasoning` object with `effort`/`max_tokens`/`exclude`/`enabled` sub-fields [[OpenRouter-4]](references.md#openrouter). `enabled: false` is honored as an explicit opt-out — no lift occurs. `max_tokens`, `exclude`, and `enabled: true` are silent-dropped (no documented sink on non-reasoning routes). Top-level `reasoning_effort` wins on conflict.

**When to restore**: n/a — this is permanent normalization.

**Fix (client-side)**: send `reasoning_effort` directly; this skips both this translation and the subsequent `#strip-reasoning_effort` enum validation path.

**Captured-requests**: n/a — no captures observed.

---

### #coerce-tool-choice-required

**What**: `tool_choice: "required"` silently rewritten to `tool_choice: "auto"`.

**Why**: `"required"` is temporarily disabled by network policy due to historical cost-amplifier behavior and engine-wedge observations. Coercing to `"auto"` keeps OpenAI-spec-compatible clients working transparently — the OpenAI Chat Completions reference [[OpenAI-1]](references.md#openai) documents both `"auto"` and `"required"` as valid values.

**When to restore**: when network policy re-enables `"required"` — remove the coerce in `ToolsValidator.Validate`.

**Fix (client-side)**: if you need true `"required"` semantics, file a network request. The gateway currently provides best-effort `"auto"` instead.

**Captured-requests**: n/a — no captures observed.

---

### #coerce-n-when-temperature-zero

**What**: `n: <N>` coerced to `n: 1` whenever `temperature == 0`.

**Why**: vLLM rejects `n > 1` with `temperature == 0` — greedy sampling produces identical completions, so vLLM treats this as a malformed request (`Best of with temperature 0` error). Rather than returning a 400, the gateway silently rounds down to `n: 1`, matching the sole semantically valid value under deterministic sampling.

**When to restore**: when vLLM relaxes the constraint.

**Fix (client-side)**: either set `temperature > 0` (typical) or accept `n: 1` — deterministic sampling produces one output anyway.

**Captured-requests**: n/a — no captures observed.

---

### #unwrap-extra_body

**What**: `extra_body: {keyA: valueA, ...}` envelope opened; each inner key lifted to the top level of the request document; envelope removed.

**Why**: the OpenAI Python SDK convention is to flatten `extra_body` client-side into the JSON body before the HTTP call [[OpenAI-5]](references.md#openai) — a literal `extra_body` key on the wire indicates either a non-flattening client (e.g. some LiteLLM passthrough configs) or hand-written code that copied the SDK construct verbatim. The catalog pre-pass lifts inner keys before `rejectUnknownParameters` runs; lifted keys flow through normal validation. Top-level keys win on conflict. Nested `extra_body` inside `extra_body` is not re-lifted (no recursion). Non-object envelopes (`extra_body: "x"` / `null` / `[]` / `42`) are silently dropped.

**When to restore**: n/a — unwrap is the canonical SDK-compat behavior; no restore path needed.

**Fix (client-side)**: pre-flatten in your client (correct OpenAI SDK usage); or trust the unwrap.

**Captured-requests**: n/a — no captures observed.

## Hard rejects (HTTP 400)

### #reject-unknown-param

**What**: HTTP 400, `feature "<name>" is currently rejected by the Gonka network. Some non-standard parameters can crash the vLLM engine on Gonka Host MLNodes, so the network rejects parameters that are not explicitly supported (see: https://github.com/gonka-ai/gonka/blob/main/docs/chat-api/README.md). If you do not need this parameter, remove it from the request; if you need it, file a request at https://github.com/gonka-ai/gonka/issues`.

**Why**: Closed-allowlist policy at the gateway. vLLM's `extra='allow'` model can crash the engine when unknown fields hit certain code paths; the conservative gate keeps the network stable. See the [vLLM project](https://github.com/vllm-project/vllm) for the upstream `extra='allow'` behavior.

**When to restore**: n/a — policy-level decision.

**Fix (client-side)**: drop the unknown field from your request body; if you need it, file an issue at https://github.com/gonka-ai/gonka/issues.

**Captured-requests**: n/a (this is the catch-all gate; specific captures are listed per individual rejected field below).

---

### #reject-duplicate-tool-call-id

**What**: HTTP 400, `messages[N].tool_calls[M].id is duplicated`.

**Why**: The OpenAI Chat Completions spec requires each `tool_calls[].id` within an assistant message to be unique [[OpenAI-1]](references.md#openai). The same constraint is enforced by the Anthropic Messages API — Bedrock-served Claude returns `ValidationException: messages.N.content contain duplicate Ids: tooluse_...` (see [LiteLLM issue #15178](https://github.com/BerriAI/litellm/issues/15178)). The confirmed upstream source is a bug in vLLM's Kimi-K2.6 tool-call parser: `history_tool_call_cnt` is recomputed inside the per-choice loop with `n>1`, producing colliding `functions.<name>:<idx>` ids [[vLLM-14]](references.md#vllm). Captured-requests evidence (e.g. req-1779369319274519506-325651) shows agents returning multiple distinct tool results for the same duplicated id — silent gateway-side dedup or rename would therefore risk information loss by discarding one of the real outputs.

**When to restore**: when the upstream vLLM Kimi-K2 parser fixes the counter-collision bug AND the OpenAI spec relaxes its uniqueness requirement — neither is likely in the near term.

**Fix (client-side)**: rewrite `tool_call.id` values to the canonical `functions.<name>:<global_idx>` form per Moonshot's official guidance [[Moonshot-3]](references.md#moonshot), OR rewrite to fresh UUIDs per the [OpenAI community workaround](https://community.openai.com/t/chatgpt-occasionally-reuses-tool-ids-in-the-same-session/577207). Do NOT deduplicate by ID lookup — both calls may have produced real distinct results.

**Captured-requests**: May 2026 batch — 14 captures (e.g. req-1779369319274519506-325651).

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

**Captured-requests**: n/a — no captures observed.

---

### #reject-guided-decoding

**What**: HTTP 400 on any of `guided_json`, `guided_regex`, `guided_grammar`, `guided_choice`.

**Why**: These are vLLM-native structured-output fields superseded by the `structured_outputs` envelope [[vLLM-18]](references.md#vllm). Accepting them would bypass the `response_format` xgrammar bounds enforced to mitigate CVE-2025-48944 [[CVE-2]](references.md#security-advisories); the guided-decoding fields share the same grammar-compiler attack surface and would need their own equivalent validators before they could be shipped safely.

**When to restore**: if dedicated validators with bounds equivalent to the `response_format` / `structured_outputs` validators are written.

**Fix (client-side)**: use `response_format` with `type: "json_schema"` for the same structured-output intent — see the OpenAI Chat Completions reference [[OpenAI-1]](references.md#openai) for the schema.

**Captured-requests**: n/a — no captures observed.

---

### #reject-enforced_tokens

**What**: HTTP 400 on `enforced_tokens`.

**Why**: `enforced_tokens` is a vLLM-native field for forcing specific token ids during generation. No validator has been written; there is no observed client demand. Without a validator the field could be used to skip generation entirely (security and abuse concern), so the conservative position is to reject until bounds are defined.

**When to restore**: if a validator is written with bounds (max token count, blacklist of sensitive token ids).

**Fix (client-side)**: use `response_format`, `structured_outputs`, or system-prompt instructions for output control instead.

**Captured-requests**: n/a — no captures observed.

---

### #reject-vllm-internals

**What**: HTTP 400 on any of `allowed_token_ids`, `ignore_eos`, `use_beam_search`, `truncate_prompt_tokens`, `prompt_logprobs`.

**Why**: These vLLM-native fields either pose safety or abuse risks or expose internal generation state: `allowed_token_ids` can constrain the output vocabulary in ways that bypass safety layers; `ignore_eos` lets a client request unbounded generation; `use_beam_search` is deprecated upstream; `truncate_prompt_tokens` could manipulate billing or quota accounting; `prompt_logprobs` leaks internal token-probability state. Conservative rejection is safer than partial support for any of these.

**When to restore**: if a specific use case justifies one of these fields AND a validator with appropriate bounds is written.

**Fix (client-side)**: drop these fields; the gateway will not honor them.

**Captured-requests**: n/a — no captures observed.

---

### #reject-structured_outputs-with-response_format

**What**: HTTP 400 on requests that set BOTH `structured_outputs` and `response_format` — error message `structured_outputs: cannot be combined with response_format`.

**Why**: vLLM 0.20.0 merges `response_format` into `structured_outputs` via `dataclasses.replace()` ([vllm/entrypoints/openai/chat_completion/protocol.py:455-487](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/chat_completion/protocol.py)). The merged dataclass then trips `StructuredOutputsParams.__post_init__`'s exactly-one rule and surfaces as a 400 with a leaky pydantic dump that exposes private internal fields (`_backend`, `_backend_was_auto`, `disable_fallback`). Gateway 400 pre-empts the broker round-trip (no node lock, no quota burn) and returns a clean targeted error. Forward-compat: contract stays stable if vLLM changes merge semantics in a future release.

**When to restore**: never as-is — the conflict is fundamental to vLLM's merge logic. If vLLM ever defines explicit precedence and exposes it as a documented field, the gateway could honor it instead of rejecting.

**Fix (client-side)**: send only one of the two. `response_format` is the OpenAI-standard route for JSON / json_schema outputs; `structured_outputs` is the vLLM-extension route for regex / grammar / choice / structural_tag. If you need both styles, pick the one the rest of your client toolchain understands.

**Captured-requests**: n/a — no captures observed.

---

### #reject-structured_outputs-kimi

**What**: HTTP 400 on `structured_outputs` when the route resolves to `moonshotai/Kimi-K2.6`. (Other routes accept `structured_outputs` normally.)

**Why**: Per-route gate inside `StructuredOutputsValidator`. The Moonshot Kimi Chat Completion API [[Moonshot-1]](references.md#moonshot) does not declare `structured_outputs` in its schema; forwarding the field to the vLLM Kimi-K2 path can crash the engine. The validator's `RejectedModels` list includes Kimi-K2.6 explicitly. Other routes (e.g. Qwen3-Instruct) accept `structured_outputs` via the standard xgrammar path.

**When to restore**: when Moonshot declares `structured_outputs` (or an equivalent) in their Kimi API contract.

**Fix (client-side)**: use `response_format` with `type: "json_schema"` for Kimi-K2.6 (xgrammar-based, supported on this route); use `structured_outputs` only for non-Kimi routes.

**Captured-requests**: n/a — no captures observed.

## Per-model gotchas

Brief pointers to deeper notes in per-model docs:

- **Kimi-K2.6**: [Known model-side bugs we work around](kimi-k2.6.md#known-model-side-bugs-we-work-around)
- **Qwen3-235B-A22B-Instruct-2507**: [Known model-side bugs we work around](qwen3-235b-a22b-instruct-2507.md#known-model-side-bugs-we-work-around)
