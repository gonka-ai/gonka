# Qwen3-235B-A22B-Instruct-2507 (`Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`) — overrides & extensions

Provider: Alibaba (Qwen). This doc documents how Qwen3-235B-A22B-Instruct-2507 deviates from the [universal contract](README.md). For params that behave the same as universal, see the universal contract directly.

## Model facts

| Property | Value | Source |
|----------|-------|--------|
| Provider | Alibaba (Qwen) | [[Qwen-1]](references.md#qwen) |
| vLLM route id | `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` | — |
| Context window | 128K tokens | [[Qwen-1]](references.md#qwen) |
| Tool-call parser (vLLM) | `hermes` | [[vLLM-1]](references.md#vllm) |
| Native thinking | no (Instruct variant; thinking-capable Qwen3-Thinking variant uses `chat_template_kwargs.enable_thinking`) | [[Qwen-3]](references.md#qwen) |

## Deployment requirements

Infrastructure-level constraints that must hold BEFORE this route is served — these are enforced by vLLM engine configuration / flags, NOT by the gateway:

- **Tool-call parser MUST be `hermes`** — `qwen3_coder` parser has an RCE via `eval()` on tool-call arguments ([[CVE-7]](references.md#security-advisories)). Configure with `--tool-call-parser hermes`. Never use `qwen3_coder` on this route.
- **Disable `pythonic` tool-call parser** — same ReDoS as Kimi route ([[CVE-1]](references.md#security-advisories)).
- **Speculative decoding with `extract_hidden_states` must be off** — combination crashes EngineCore on this model.
- **Pin vLLM ≥ 0.20.0** — same penalty-field stability fix as Kimi ([[CVE-11]](references.md#security-advisories)).

## Parameter overrides

*Delta from [chat-api.md universal contract](README.md#supported-parameters-universal-behavior). Listed params behave differently on this route; everything else matches universal.*

| Param | Universal | On Qwen3-235B | Why |
|-------|-----------|---------------|-----|
| `tools[].function.strict` | (silent-strip in universal via `ToolsValidator`) | silent-strip — vLLM `hermes` parser ignores | [[vLLM-1]](references.md#vllm) |

*(No other Qwen-specific overrides at this time — frequency_penalty / presence_penalty use the universal clamp behavior; safety_identifier is universal-stripped; thinking is not supported on the Instruct variant.)*

## Native extensions

*Params unique to this route — no equivalent in the universal contract.*

| Param | Type | Behavior | Source |
|-------|------|----------|--------|
| `chat_template_kwargs.enable_thinking` | bool | Pass-through; activates Qwen3-Thinking-variant chain-of-thought on `<think>` tokens (no-op on the Instruct variant currently routed). | [[Qwen-3]](references.md#qwen) |
| `chat_template_kwargs.preserve_thinking` | bool | Pass-through; Qwen-specific knob | [[Qwen-3]](references.md#qwen) |

Note: top-level `enable_thinking` (without the `chat_template_kwargs.` prefix) is **translated** to `chat_template_kwargs.enable_thinking` by the universal contract — see [translate-enable_thinking](troubleshooting.md#translate-enable_thinking).

## Structured outputs

| Field | Status | Note |
|-------|--------|------|
| `response_format` | ✅ supported (see universal) | xgrammar via vLLM; full schema bounds enforced |
| `structured_outputs` | ✅ supported on this route | Full envelope validated via `StructuredOutputsValidator`. See sub-field caps below. |

**`structured_outputs` sub-field caps** (CVE-driven):

| Sub-field | Cap | CVEs |
|-----------|-----|------|
| `json` | Same xgrammar bounds as `response_format` (depth ≤16, nodes ≤128, branch ≤16, enum ≤256, size ≤16 KiB; `$ref`/`$defs`/`definitions` forbidden; `pattern` ≤512 B + regex-compile). Rejects upstream-legal `json: string` form — clients must pre-parse. | [[CVE-2]](references.md#security-advisories), [[CVE-3]](references.md#security-advisories) |
| `regex` | (same regex-compile + pattern-length checks as `response_format` pattern fields) | [[CVE-2]](references.md#security-advisories) |
| `choice` | ≤256 entries × 1 KiB each | [[CVE-4]](references.md#security-advisories) |
| `grammar` | ≤8 KiB + active bracket-nesting depth ≤200 | [[CVE-8]](references.md#security-advisories) |
| `structural_tag` | ≤4 KiB | (no CVE history) |
| `json_object` | (no params; bool/empty-object shape only) | — |

Enforces vLLM's `exactly-one-of` constraint at the gateway: only one of `json`/`regex`/`choice`/`grammar`/`json_object`/`structural_tag` may be present (wire-explicit `null` counts as absent). 400 if `response_format` is also set. Closed allow-list inside the envelope — unknown sub-keys return 400.

## Known model-side bugs we work around

- **`thinking_token_budget` crashes vLLM `EngineCore` when forwarded** to Qwen3-Instruct-2507 (observed `EngineDeadError`). Mitigated by `ModelScopedParameterHandler` at PreValidation — the field is silently stripped for every non-Kimi model. Restoration depends on a thinking-capable Qwen variant being added.
- No other known model-emission bugs at this time. The Instruct variant is non-thinking, so the `<think>` tag emission bugs that affect thinking-capable models do not apply.

## See also
- [Troubleshooting](troubleshooting.md)
- [References](references.md)
- [Universal contract](README.md)
