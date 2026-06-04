# Kimi-K2.6 (`moonshotai/Kimi-K2.6`) — overrides & extensions

Provider: Moonshot AI. This doc documents how Kimi-K2.6 deviates from the [universal contract](README.md). For params that behave the same as universal, see the universal contract directly.

## Model facts

| Property | Value | Source |
|----------|-------|--------|
| Provider | Moonshot AI | [[Moonshot-1]](references.md#moonshot) |
| vLLM route id | `moonshotai/Kimi-K2.6` | — |
| Context window | 256K tokens | [[Moonshot-2]](references.md#moonshot) |
| Tool-call parser (vLLM) | `kimi_k2` | [[vLLM-1]](references.md#vllm) |
| Native thinking | yes (via `chat_template_kwargs.thinking`) | [[Moonshot-3]](references.md#moonshot) |

## Deployment requirements

Infrastructure-level constraints that must hold BEFORE this route is served — these are enforced by vLLM engine configuration / flags, NOT by the gateway:

- **Pin vLLM ≥ 0.20.0** — earlier versions crash `EngineCore` with `extract_hidden_states` when penalty fields are forwarded ([[CVE-11]](references.md#security-advisories)). Gateway fallback: if pinned vLLM version is older, also reject `repetition_penalty ≠ 1.0` at the gateway boundary.
- **Disable `pythonic` tool-call parser** — ReDoS via crafted Python-syntax tool calls ([[CVE-1]](references.md#security-advisories)). The Kimi-K2.6 route MUST use `--tool-call-parser kimi_k2`.
- **Open TODO: CVE-2026-44222 multimodal sanitizer** — Kimi-K2.6's chat template accepts `image_url` / `video_url` content parts; the gateway currently validates text-only. Multimodal content parts are rejected at the message validator. A special-token literal sanitizer is needed before lifting this restriction. Track via [[CVE-10]](references.md#security-advisories).

## Parameter overrides

*Delta from [chat-api.md universal contract](README.md#supported-parameters-universal-behavior). Listed params behave differently on this route; everything else matches universal.*

| Param | Universal | On Kimi-K2.6 | Why |
|-------|-----------|--------------|-----|
| `safety_identifier` | strip | **pass-through** (string, ≤512 B) | Moonshot consumes the field for abuse tracking on their hosted backend [[Moonshot-1]](references.md#moonshot) |
| `frequency_penalty` | clamp [-2, 2] | **force-rewrite to `0.0`** | Moonshot's K2.6 wire accepts only `0.0`; model-side constraint [[Moonshot-1]](references.md#moonshot) |
| `presence_penalty` | clamp [-2, 2] | **force-rewrite to `0.0`** | same as above [[Moonshot-1]](references.md#moonshot) |
| `tools[].function.strict` | (silent-strip in universal via `ToolsValidator`) | silent-strip — vLLM `kimi_k2` parser ignores | [[vLLM-1]](references.md#vllm) |

## Native extensions

*Params unique to this route — no equivalent in the universal contract.*

| Param | Type | Behavior | Source |
|-------|------|----------|--------|
| `thinking` | object | `{type: "enabled"\|"disabled"\|"adaptive"\|"auto"}`. `adaptive`/`auto` are client-side opt-in (Claude Code CLI / Kimi clients) and resolve to enabled. Validator mirrors the resolved boolean into `chat_template_kwargs.thinking` and **drops top-level `thinking`** — the chat template only reads from kwargs. Sibling `display` field is silent-stripped ([why](troubleshooting.md#strip-display-thinking-sibling)). | [[Anthropic-1]](references.md#anthropic), [[Moonshot-1]](references.md#moonshot) |
| `thinking_token_budget` | int | Injects `max_tokens / 2` default; clamp ≤ 96 000 and ≤ `max_tokens` (96k matches Moonshot's HLE/AIME reasoning budget) | [[Moonshot-3]](references.md#moonshot) |
| `messages[].reasoning_content` | string | Pass-through on assistant turns (Kimi multi-turn replay convention) | [[Moonshot-1]](references.md#moonshot) |

<details>
<summary>Thinking sub-shape details</summary>

Accepted values for `thinking.type`:
- `"enabled"` → `chat_template_kwargs.thinking = true`, top-level dropped
- `"disabled"` → `chat_template_kwargs.thinking = false`, top-level dropped
- `"adaptive"` → resolves to enabled. Claude Code CLI and Anthropic extended-thinking SDKs use this as a client-side budget extension; the canonical Anthropic wire enum is enabled|disabled only ([[Anthropic-1]](references.md#anthropic)) — the SDK is meant to resolve `adaptive` into a concrete budget before the HTTP call, but some forwarding paths leak it through.
- `"auto"` → resolves to enabled (synonym for `adaptive`).

Sibling `display` field (Claude Code UI hint, e.g. `"summarized"`) is silent-stripped because it has no vLLM semantics — the value never reaches the chat template.

Pre-existing `chat_template_kwargs.thinking` wins on conflict (no overwrite of explicit caller intent).
</details>

## Structured outputs

| Field | Status | Note |
|-------|--------|------|
| `response_format` | ✅ supported (see universal) | xgrammar via vLLM; full schema bounds enforced |
| `structured_outputs` | ❌ **rejected on this route** | Moonshot API does not declare the field — see [why](troubleshooting.md#reject-structured_outputs-kimi) |

## Known model-side bugs we work around

- **Duplicate `tool_calls[].id` emission**: vLLM `kimi_k2` parser has a confirmed counter-collision bug when running with `n>1` — `history_tool_call_cnt` recomputed inside the per-choice loop produces colliding `functions.<name>:<idx>` ids. See [vLLM PR #21259](https://github.com/vllm-project/vllm/pull/21259) review thread. The gateway rejects duplicate ids per OpenAI spec — see [troubleshooting](troubleshooting.md#reject-duplicate-tool-call-id). Clients must rewrite ids client-side per Moonshot's official guidance [[Moonshot-3]](references.md#moonshot).
- **Empty content + `finish_reason=length` when thinking eats the budget**: at small `max_tokens` Kimi-K2.6 routinely consumes the entire budget inside `<think>...</think>` and returns visible content as empty. Mitigated by `thinking_token_budget` default injection (see Native extensions above) — gateway injects `max_tokens / 2` when the field is absent, leaving half the budget for visible content.

## See also
- [Troubleshooting](troubleshooting.md)
- [References](references.md)
- [Universal contract](README.md)
