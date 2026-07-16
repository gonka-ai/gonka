# Client agent compatibility

Compatibility status of popular OpenAI-format clients (OpenAI SDK Python/Node, LangChain, LiteLLM, Claude Code CLI, Cline, kimi-cli, AutoGen, Hermes Agent) against this gateway, with known gotchas linked to [troubleshooting](troubleshooting.md).

## Compatibility matrix

| Client | Status | Known gotchas |
|--------|--------|---------------|
| OpenAI Python SDK (`openai`) | ✅ supported | `extra_body` lifted automatically. `extra_headers` stripped if it leaks into the body — [why](troubleshooting.md#strip-extra_headers). |
| OpenAI Node SDK | ✅ supported | No `extra_body` convention — pass fields at top level. |
| LangChain (`langchain_openai.ChatOpenAI`) | ✅ supported | Empty assistant `tool_calls` slots serialized as `null` — auto-normalized. |
| LangChain + Anthropic bridge | ✅ supported | Tool-only assistant turns serialized as `content: []` — auto-normalized. |
| LiteLLM proxy | ⚠️ caveat | Non-flattening `extra_body` from passthrough configs handled via unwrap — [why](troubleshooting.md#unwrap-extra_body). Requires `--drop-params` for unsupported route params; see [LiteLLM #4769](https://github.com/BerriAI/litellm/issues/4769) for the SDK behavior context. |
| Claude Code CLI | ⚠️ caveat | Emits `thinking.type: "adaptive"` + `display: "summarized"` — normalized for Kimi route (see [Kimi native extensions](kimi-k2.6.md#native-extensions)). |
| Cline / Continue.dev | ⚠️ caveat | May emit `think: true` (Ollama-style) — silently stripped, [why](troubleshooting.md#strip-think). |
| `kimi-cli` (Moonshot) | ⚠️ caveat | Emits top-level `cache_key` even for non-Moonshot routes — silently stripped, [why](troubleshooting.md#strip-cache_key). |
| AutoGen / agno | ⚠️ caveat | If a model emits duplicate `tool_calls[].id`, the gateway rejects with 400 — client should rewrite ids client-side per [[Moonshot-3]](references.md#moonshot). See also [agno #5116](https://github.com/agno-agi/agno/issues/5116) for the canonical workaround pattern. |
| Hermes Agent (Nous) | ⚠️ caveat | `tags` field rejected — [why](troubleshooting.md#reject-tags). Their [API docs](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/api-server.md) document "standard OpenAI Chat Completions format" — drop `tags` client-side. |
| Custom (raw HTTP) | ⚠️ DIY | Closed allowlist — check [unsupported parameters](README.md#unsupported-parameters-http-400) before sending unfamiliar fields. |

## Common gotchas by symptom

### "My `tool_call` is being rejected as duplicate"

Cause: model (typically Kimi-K2.6) emitted duplicate symbolic ids in one assistant turn. Fix: rewrite ids to canonical `functions.<name>:<global_idx>` form before sending — see [[Moonshot-3]](references.md#moonshot) for the official guidance and [troubleshooting](troubleshooting.md#reject-duplicate-tool-call-id) for the gateway-side rationale. Do NOT deduplicate by ID lookup — both calls may have produced real distinct results.

### "My `thinking` config is ignored on Kimi"

Cause: Kimi-K2.6 reads from `chat_template_kwargs.thinking` only — the gateway mirrors automatically from top-level `thinking.type`. The top-level field is dropped after mirroring. To override the mirrored value, pre-set `chat_template_kwargs.thinking` directly in your request body.

### "My tool message is rejected on MiniMax-M2.7"

Cause: MiniMax-M2.7 uses a different `role:"tool"` contract from OpenAI — `content` must be an array of `{name, type:"text", text}` entries, with no `tool_call_id`. Bare-string `content` or OpenAI-style `tool_call_id`-correlated messages are rejected with HTTP 400. See [accept-tool-message-minimax-shape](troubleshooting.md#accept-tool-message-minimax-shape) and the [MiniMax-M2.7 per-model doc](minimax-m2.7.md). Clients dual-emitting `tool_call_id` for portability across routes are fine — the gateway silently strips it.

### "My `<think>` blocks disappeared on MiniMax-M2.7"

They shouldn't. MiniMax-M2.7 emits reasoning inline as `<think>...</think>` in `content`, and the gateway preserves these byte-identical across multi-turn history. If you're seeing them stripped, you're probably running them through a client-side reasoning filter — keep them in conversation history for chain-of-thought continuity ([[MiniMax-5]](references.md#minimax)).

### "My cache key disappeared"

Cause: `cache_key` / `prompt_cache_key` silently stripped. vLLM uses a different field (`cache_salt`) for cache isolation, and the aliasing PR is unmerged — see [troubleshooting](troubleshooting.md#strip-cache_key) for the full chain of upstream gaps.

### "Where do unsupported sampling fields go?"

`allowed_token_ids`, `ignore_eos`, `use_beam_search`, `truncate_prompt_tokens`, `prompt_logprobs` are all rejected with HTTP 400 — see [troubleshooting](troubleshooting.md#reject-vllm-internals). For output control, use `response_format` or `structured_outputs` (on supported routes).

### "Why does my `temperature: 0` + `n: 5` request return only one completion?"

The gateway coerces `n` to `1` when `temperature == 0` because vLLM rejects `n > 1` at that temperature (greedy sampling produces identical completions anyway). See [troubleshooting](troubleshooting.md#coerce-n-when-temperature-zero).

### "Why is `tool_choice: \"required\"` being treated as `\"auto\"`?"

Network policy temporarily disables `"required"` due to historical engine-wedge / cost-amplifier behavior. The gateway silently coerces to `"auto"` to preserve client compatibility. See [troubleshooting](troubleshooting.md#coerce-tool-choice-required).
