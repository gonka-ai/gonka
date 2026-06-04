# Gonka Chat Completions API

OpenAI-compatible chat completions, routed to Kimi-K2.6 / Qwen3-235B via vLLM. This doc covers universal parameter behavior. For per-model overrides see [Kimi-K2.6](kimi-k2.6.md) / [Qwen3-235B](qwen3-235b-a22b-instruct-2507.md).

## Quick navigation
- [Per-model overrides: Kimi-K2.6](kimi-k2.6.md)
- [Per-model overrides: Qwen3-235B-A22B-Instruct-2507](qwen3-235b-a22b-instruct-2507.md)
- [Why was my param stripped/rejected?](troubleshooting.md)
- [Client agents compatibility](agents.md)
- [Source citations](references.md)

## Endpoint
`POST /v1/chat/completions` — request/response shape per [[OpenAI-1]](references.md#openai).

## Request limits

| Limit | Value | Source |
|-------|-------|--------|
| Max body size | 10 MiB | gateway-level; pre-`json.Unmarshal` check |
| Max nesting depth | 32 | `ensureRequestNestingDepth`; defense against deeply-nested JSON DoS |
| Max messages count | 2048 | OpenAI Chat Completions convention; defensive cap |
| Max choices (`n`) | 5 | `MaxChatRequestChoices`; ceiling on completion fan-out |

## Supported parameters (universal behavior)

| Param | Type | Default | Behavior | Source |
|-------|------|---------|----------|--------|
| `model` | string | — | Required; route key | [[OpenAI-1]](references.md#openai) |
| `messages` | array | — | Required; OpenAI shape (see [Messages contract](#messages-contract)); ≤2048 entries | [[OpenAI-1]](references.md#openai) |
| `temperature` | float | 1.0 | cap ≤ 2.0; sanitize (NaN/Inf strip) | [[OpenAI-1]](references.md#openai) |
| `top_p` | float | 1.0 | sanitize (NaN/Inf strip) | [[OpenAI-1]](references.md#openai) |
| `top_k` | int | — | sanitize float; vLLM extension | [[vLLM-1]](references.md#vllm) |
| `min_p` | float | — | sanitize; vLLM extension | [[vLLM-1]](references.md#vllm) |
| `frequency_penalty` | float | 0.0 | clamp `[-2, 2]`; see [Kimi override](kimi-k2.6.md#parameter-overrides) for force-rewrite | [[OpenAI-1]](references.md#openai) |
| `presence_penalty` | float | 0.0 | clamp `[-2, 2]`; see [Kimi override](kimi-k2.6.md#parameter-overrides) for force-rewrite | [[OpenAI-1]](references.md#openai) |
| `repetition_penalty` | float | 1.0 | cap ≤ 2.0; vLLM extension | [[vLLM-1]](references.md#vllm) |
| `logit_bias` | object | — | ≤1024 entries; value range `[-100, 100]` | [[OpenAI-1]](references.md#openai) |
| `max_tokens` | int | — | clamp | [[OpenAI-1]](references.md#openai) |
| `max_completion_tokens` | int | — | alias for max_tokens | [[OpenAI-1]](references.md#openai) |
| `stream` | bool | false | pass-through | [[OpenAI-1]](references.md#openai) |
| `stream_options` | object | — | strip when stream≠true; whitelist `include_usage`; strip `continuous_usage_stats` | [[OpenAI-1]](references.md#openai) |
| `stop` | str\|array | — | pass-through; ≤16 entries × 256 B each | [[OpenAI-1]](references.md#openai) |
| `n` | int | 1 | hard cap ≤5; coerce to 1 when temperature==0 ([why](troubleshooting.md#coerce-n-when-temperature-zero)) | [[OpenAI-1]](references.md#openai), [[CVE-9]](references.md#security-advisories) |
| `seed` | uint64 | — | pass-through | [[OpenAI-1]](references.md#openai) |
| `logprobs` | bool | — | force `true`; observability pipeline | — |
| `top_logprobs` | int | — | force `5`; observability pipeline | — |
| `return_token_ids` | bool | — | force `true`; required for stream-derived `enforced_tokens` reconstruction on Kimi-K2.6 reasoning routes (without it, `<think>`/`</think>` are silently dropped from SSE while still counted in `usage.completion_tokens`). Resulting `prompt_token_ids` / `choices[].token_ids` are stripped from the client-facing response | [[vLLM-19]](references.md#vllm), [[vLLM-20]](references.md#vllm) |
| `response_format` | object | — | shape-bounded (depth ≤16, nodes ≤128, branch arms ≤16, enum ≤256, size ≤16 KiB); `$ref`/`$defs`/`definitions` forbidden; `pattern` ≤512 B + must compile as regex; `json_schema.name` non-empty ≤64 chars matching `^[A-Za-z0-9_.-]+$`; schema must be an object | [[OpenAI-1]](references.md#openai), [[CVE-2]](references.md#security-advisories) |
| `structured_outputs` | object | — | validated against vLLM envelope (`json`/`regex`/`choice`/`grammar`/`json_object`/`structural_tag`); CVE-driven caps per sub-field — see [Qwen native extensions](qwen3-235b-a22b-instruct-2507.md#native-extensions); **rejected on Kimi-K2.6 route** ([why](troubleshooting.md#reject-structured_outputs-kimi)); **rejected if combined with `response_format`** ([why](troubleshooting.md#reject-structured_outputs-with-response_format)) | [[vLLM-16]](references.md#vllm), [[vLLM-18]](references.md#vllm), [[CVE-3]](references.md#security-advisories), [[CVE-4]](references.md#security-advisories), [[CVE-8]](references.md#security-advisories) |
| `tools` | array | — | shape-bounded: function schema depth ≤16, nodes ≤256, branch arms ≤16, enum ≤256, size ≤16 KiB; `$ref`/`$defs`/`definitions` forbidden; `pattern` ≤512 B + regex compile; `function.name` ≤64 B; `tools[].function.strict` silent-stripped (vLLM parsers ignore) | [[OpenAI-1]](references.md#openai), [[CVE-2]](references.md#security-advisories) |
| `tool_choice` | string\|object | "auto" if tools | shape-strict; `function.name` ≤64 B; `"required"` coerced ([why](troubleshooting.md#coerce-tool-choice-required)) | [[OpenAI-1]](references.md#openai) |
| `parallel_tool_calls` | bool | — | pass-through | [[OpenAI-1]](references.md#openai) |
| `user` | string | — | byte-length ≤512 | [[OpenAI-1]](references.md#openai) |
| `safety_identifier` | string | — | silent-strip; see [Kimi override](kimi-k2.6.md#parameter-overrides) for pass-through ([why](troubleshooting.md#strip-safety_identifier)) | [[OpenAI-6]](references.md#openai) |
| `metadata` | object | — | OpenAI bounds: ≤16 keys × 64-char × 512-char vals | [[OpenAI-1]](references.md#openai) |
| `service_tier` | string | — | silent-strip ([why](troubleshooting.md#strip-service_tier)) | [[OpenAI-2]](references.md#openai), [[OpenAI-3]](references.md#openai) |
| `store` | bool | — | silent-strip ([why](troubleshooting.md#strip-store)) | [[OpenAI-1]](references.md#openai) |
| `provider` | object | — | silent-strip ([why](troubleshooting.md#strip-provider)) | [[OpenRouter-3]](references.md#openrouter) |
| `plugins` | array | — | silent-strip ([why](troubleshooting.md#strip-plugins)) | [[OpenRouter-2]](references.md#openrouter) |
| `prompt_cache_key` | string | — | silent-strip ([why](troubleshooting.md#strip-prompt_cache_key)) | [[OpenAI-1]](references.md#openai), [[Moonshot-1]](references.md#moonshot) |
| `cache_key` | string | — | silent-strip ([why](troubleshooting.md#strip-cache_key)) | [[Moonshot-1]](references.md#moonshot) |
| `extra_headers` | object | — | silent-strip ([why](troubleshooting.md#strip-extra_headers)) | [[OpenAI-5]](references.md#openai) |
| `extra_body` | object | — | unwrap to top-level ([why](troubleshooting.md#unwrap-extra_body)) | [[OpenAI-5]](references.md#openai) |
| `reasoning_effort` | enum string | — | validated then stripped ([why](troubleshooting.md#strip-reasoning_effort)) | [[vLLM-1]](references.md#vllm), [[OpenAI-4]](references.md#openai) |
| `reasoning` | object | — | translate `effort` → `reasoning_effort` ([why](troubleshooting.md#translate-reasoning)) | [[OpenRouter-4]](references.md#openrouter) |
| `enable_thinking` | bool | — | translate to chat_template_kwargs ([why](troubleshooting.md#translate-enable_thinking)) | [[Qwen-3]](references.md#qwen) |
| `thinking_config` | object | — | silent-strip ([why](troubleshooting.md#strip-thinking_config)) | — |
| `think` | bool | — | silent-strip ([why](troubleshooting.md#strip-think)) | — |
| `min_tokens` | int | — | vLLM extension; clamp to ≤max_tokens; conditional strip when stop_token_ids set | [[vLLM-1]](references.md#vllm) |
| `bad_words` | string array | — | vLLM extension; ≤64 entries × 128 B per entry | [[vLLM-1]](references.md#vllm) |
| `stop_token_ids` | int array | — | vLLM extension; ≤64 | [[vLLM-1]](references.md#vllm) |
| `skip_special_tokens` | bool | — | vLLM extension; pass-through | [[vLLM-1]](references.md#vllm) |
| `detokenize` | bool | — | vLLM extension; pass-through | [[vLLM-1]](references.md#vllm) |
| `chat_template_kwargs` | object | — | depth ≤16, nodes ≤128, size ≤16 KiB; key denylist (`chat_template`, `tokenize`, `tools`, `documents`, `conversation`, `continue_final_message`, `padding`, `truncation`, `max_length`, `return_tensors`, `return_dict`) — CVE-2025-61620 / CVE-2025-62426 mitigation | [[vLLM-1]](references.md#vllm), [[CVE-5]](references.md#security-advisories), [[CVE-6]](references.md#security-advisories) |

*Parameters with truly model-exclusive behavior (`thinking`, `thinking_token_budget`, `messages[].reasoning_content`) are documented in the per-model docs — see [Kimi-K2.6](kimi-k2.6.md) and [Qwen3-235B-A22B-Instruct-2507](qwen3-235b-a22b-instruct-2507.md). For params with universal baseline behavior plus per-model adjustments (`frequency_penalty`, `presence_penalty`, `safety_identifier`), the baseline appears above and the per-model override is linked from the row.*

## Unsupported parameters (HTTP 400)

| Param | Origin | Why | Details |
|-------|--------|-----|---------|
| `guided_json` / `guided_regex` / `guided_grammar` / `guided_choice` | vLLM-native | bypasses `response_format` xgrammar bounds | [why](troubleshooting.md#reject-guided-decoding) |
| `enforced_tokens` | vLLM-native | no validator written | [why](troubleshooting.md#reject-enforced_tokens) |
| `tags` | folk convention | not in any served contract | [why](troubleshooting.md#reject-tags) |
| `allowed_token_ids` / `ignore_eos` / `use_beam_search` / `truncate_prompt_tokens` / `prompt_logprobs` | vLLM-native | safety / unsupported | [why](troubleshooting.md#reject-vllm-internals) |
| `structured_outputs` (on Kimi-K2.6 route) | vLLM-native | Moonshot API does not declare | [why](troubleshooting.md#reject-structured_outputs-kimi) |
| Unknown top-level params | — | closed-allowlist policy | [why](troubleshooting.md#reject-unknown-param) |
| Duplicate `tool_calls[].id` within one assistant message | OpenAI spec violation | per-spec reject | [why](troubleshooting.md#reject-duplicate-tool-call-id) |

## Messages contract

Enforced by the gateway's message validator:

- Roles: `developer`, `system`, `user`, `assistant`, `tool`, `function`.
- Assistant `content` may be empty/null only when `tool_calls` or `function_call` is present.
- Tool messages require `tool_call_id` matching a prior assistant `tool_calls[].id`.
- Function messages require `name`.
- Content parts: only `{"type": "text", "text": "..."}` is accepted. Typed arrays of text parts are flattened to a single string before forwarding.
- Empty tool `content` is normalized to a sentinel string; missing tool `content` is also normalized.
- **Lenient SDK compat:** explicit JSON `null` for assistant `tool_calls` / `function_call` is treated as field-absent and the key is dropped before forwarding. OpenAI-Python and several LangChain-derived clients serialize empty slots as `null`; rejecting was a gateway-side false-positive.
- **Lenient SDK compat:** `name` on `role: "tool"` messages is silently stripped before validation. The field was required in the legacy `role: "function"` API; modern [[OpenAI-1]](references.md#openai) documents only `role` / `content` / `tool_call_id` on tool messages, and vLLM ignores extra keys.
- **Lenient SDK compat:** empty-array `content: []` is treated the same as `null` / `""` — whitespace string, empty string, and empty array all normalize uniformly. SDK bridges (notably Anthropic ↔ OpenAI translation layers) emit `[]` for tool-call-only assistant turns instead of `null`.
- **Lenient SDK compat:** orphan `role: "tool"` messages — those whose `tool_call_id` was never emitted by a prior `assistant.tool_calls[].id` — are silently dropped before validation. Long agent conversations sometimes lose part of a multi-tool fan-out during client-side history compaction.
- **Lenient SDK compat:** empty `role: "assistant"` turns — no `content` AND no `tool_calls` AND no `function_call` — are silently dropped. The model can't observe an informationless turn; the drop is a semantic no-op.
- **Strict (no lenient compat):** duplicate `tool_calls[].id` within a single assistant message is rejected per OpenAI spec — see [troubleshooting](troubleshooting.md#reject-duplicate-tool-call-id).

## Errors

| HTTP | When |
|------|------|
| 400 | rejected parameter, shape violation, duplicate `tool_call.id`, malformed body |
| 4xx / 5xx | proxied from vLLM upstream (model errors, OOM, etc.) |

## Conventions

**Status icons** (used in per-model and agents tables):

| Icon | Meaning |
|------|---------|
| ✅ | supported, pass-through with documented bounds |
| 🔧 | supported with active transformation (clamp / coerce / translate / mirror) |
| ⚠️ | accepted on the wire, silently stripped before forwarding |
| ❌ | rejected (HTTP 400) |
| ➖ | not applicable / not emitted on this surface |

**Footnote marker namespaces**:

```
[OpenAI-N]      OpenAI
[Anthropic-N]   Anthropic
[vLLM-N]        vLLM
[Moonshot-N]    Moonshot (includes Kimi model line)
[Qwen-N]        Qwen
[OpenRouter-N]  OpenRouter
[CVE-N]         security advisories
```

Resolved in [references.md](references.md). Industry/community sources (Ollama blog, OpenAI community threads, arxiv papers) appear as inline links in troubleshooting/agents, not in references.md.

**Anchor convention**: troubleshooting anchors are `#<verb>-<param>` with verb ∈ {strip, reject, translate, coerce, unwrap, force}. Other docs use descriptive kebab-case. Once published, an anchor is never renamed — additions only.
