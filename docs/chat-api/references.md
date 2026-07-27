# References

Footnote markers used across `chat-api.md`, per-model docs, and `troubleshooting.md`.

Namespaces:
- `[OpenAI-N]` OpenAI
- `[Anthropic-N]` Anthropic
- `[vLLM-N]` vLLM
- `[Moonshot-N]` Moonshot (includes Kimi model line)
- `[Qwen-N]` Qwen
- `[MiniMax-N]` MiniMax AI (MiniMax-M2 line)
- `[SGLang-N]` SGLang (cross-engine parser bug references)
- `[OpenRouter-N]` OpenRouter
- `[CVE-N]` security advisories

Industry/community sources (Ollama blog, OpenAI community thread, arxiv papers) are inline links in `troubleshooting.md` and `agents.md`, not here. Captured-requests evidence is referenced inline by request-id. Chain governance changes (model registration, ModelArgs pins) are cited inline by PR number under the appropriate per-model doc, not in this file.

## OpenAI

- **[OpenAI-1]** [Chat Completions API reference](https://platform.openai.com/docs/api-reference/chat/create) — full param schema for `/v1/chat/completions`, including `messages`, `stream_options`, and all top-level fields.
- **[OpenAI-2]** [Flex processing guide](https://platform.openai.com/docs/guides/flex-processing) — `service_tier=flex` behaviour and SLA trade-offs.
- **[OpenAI-3]** [Priority processing guide](https://developers.openai.com/api/docs/guides/priority-processing) — `service_tier=priority` behaviour and billing.
- **[OpenAI-4]** [Reasoning guide](https://developers.openai.com/api/docs/guides/reasoning) — `reasoning_effort` concept and wire enum values.
- **[OpenAI-5]** [openai-python README — undocumented request params](https://github.com/openai/openai-python/blob/main/README.md#undocumented-request-params) — SDK convention for `extra_body`/`extra_headers`/`extra_query`; clarifies client-side flatten semantics.
- **[OpenAI-6]** [Safety identifier help-center article](https://help.openai.com/en/articles/5428082-how-to-incorporate-a-safety-identifier) — `safety_identifier` field guidance; recommends short hashed identifiers for end-user attribution.

## Anthropic

- **[Anthropic-1]** [Extended thinking docs](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking) — wire enum for `thinking.type` (`enabled`/`disabled`); basis for rejecting `adaptive`/`auto` as non-wire values.
- **[Anthropic-2]** [Handling stop reasons](https://docs.anthropic.com/en/docs/build-with-claude/handling-stop-reasons) — documents `stop_reason="max_tokens"` with empty/truncated content as an expected outcome when extended thinking consumes the budget; advises continuation rather than retry.

## vLLM

- **[vLLM-1]** [ChatCompletionRequest protocol source](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/chat_completion/protocol.py) — wire schema for `reasoning_effort` enum and other chat-completion fields.
- **[vLLM-2]** [OpenAI protocol source](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/protocol.py) — broader OpenAI-compatible endpoint schema; `service_tier` absent here, explaining silent-drop via `extra='allow'`.
- **[vLLM-3]** [Issue #16016 — Cache Salting RFC](https://github.com/vllm-project/vllm/issues/16016) — introduces `cache_salt` field for prompt-cache isolation; basis for stripping `prompt_cache_key`.
- **[vLLM-4]** [Issue #17790 — hermes JSONDecodeError](https://github.com/vllm-project/vllm/issues/17790) — hermes parser `JSONDecodeError` on multiple tool-call blobs.
- **[vLLM-5]** [Issue #27447 — enable_thinking=false breaks guided decoding](https://github.com/vllm-project/vllm/issues/27447) — `enable_thinking=false` breaks guided decoding interaction.
- **[vLLM-6]** [Issue #29814 — Qwen3 reasoning parser edge cases](https://github.com/vllm-project/vllm/issues/29814) — Qwen3 reasoning parser edge cases.
- **[vLLM-7]** [Issue #33264 — alias prompt_cache_key → cache_salt](https://github.com/vllm-project/vllm/issues/33264) — open request to alias `prompt_cache_key`/`cache_key` → `cache_salt`; unmerged as of Jan 2026.
- **[vLLM-8]** [Issue #39677 — structured output + thinking interaction](https://github.com/vllm-project/vllm/issues/39677) — structured output and thinking interaction bug.
- **[vLLM-9]** [Issue #40875 — guided-decoding/thinking bug](https://github.com/vllm-project/vllm/issues/40875) — related guided-decoding/thinking bug.
- **[vLLM-10]** [Issue #42021 — tool_call inside think breaks hermes](https://github.com/vllm-project/vllm/issues/42021) — `<tool_call>` inside `<think>` breaks hermes parsing.
- **[vLLM-11]** [OpenAI-compatible server docs](https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html) — vLLM's OpenAI-compatible endpoint feature set and supported parameters.
- **[vLLM-12]** [PR #10463 — extra='allow' on ChatCompletionRequest](https://github.com/vllm-project/vllm/pull/10463) — `extra='allow'` setting that causes unknown fields (e.g. `service_tier`) to be silently dropped.
- **[vLLM-13]** [PR #17045 — cache_salt implementation](https://github.com/vllm-project/vllm/pull/17045) — ships `cache_salt` field for prompt-cache isolation (referenced by issue #16016).
- **[vLLM-14]** [PR #21259 — duplicate tool_call_id bug](https://github.com/vllm-project/vllm/pull/21259) — `history_tool_call_cnt` recomputation inside per-choice loop causes id collisions with `n>1`.
- **[vLLM-15]** [Releases page](https://github.com/vllm-project/vllm/releases) — verify running image `__version__` against advisory "fixed in" notes.
- **[vLLM-16]** [sampling_params.py source](https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py) — `structured_outputs` envelope definition and exactly-one-of constraint at lines 59–80.
- **[vLLM-17]** [Security advisories index](https://github.com/vllm-project/vllm/security/advisories) — aggregate page; review on engine upgrades.
- **[vLLM-18]** [Structured outputs feature docs](https://docs.vllm.ai/en/latest/features/structured_outputs.html) — `structured_outputs` supersedes `guided_json`/`guided_regex`/`guided_grammar`/`guided_choice`.
- **[vLLM-19]** [PR #29074 — kimi_k2 reasoning parser: emit DeltaMessage when return_token_ids=true](https://github.com/vllm-project/vllm/pull/29074) — changes `extract_reasoning_streaming` to emit an empty `DeltaMessage()` (with the token id attached) instead of `None` for single-token deltas carrying `<think>`/`</think>`. Without `return_token_ids=true`, those tokens are silently dropped from the SSE stream while still counted in `usage.completion_tokens`, producing a hidden-token gap that breaks stream-derived `enforced_tokens` reconstruction.
- **[vLLM-20]** [kimi_k2_reasoning_parser.py source](https://github.com/vllm-project/vllm/blob/main/vllm/reasoning/kimi_k2_reasoning_parser.py) — the parser whose `extract_reasoning_streaming` returns `None` (= suppresses event) when a delta is exactly `<think>` or `</think>`. Tokens still counted in usage; vLLM-19 added the `return_token_ids` escape valve.
- **[vLLM-21]** [vLLM Recipes — MiniMax-M2 Series Usage Guide](https://docs.vllm.ai/projects/recipes/en/latest/MiniMax/MiniMax-M2.html) — official vLLM recipe covering M2/M2.1/M2.5/M2.7 deployment (parsers, topology, compilation-config, AMD AITER env vars).
- **[vLLM-22]** [Issue #39610 — MiniMax-M2.7/Qwen3.5 FP8 fail on A100/Ampere with nightly](https://github.com/vllm-project/vllm/issues/39610) — regression confirmed vs v0.19.0.
- **[vLLM-23]** [Issue #38212 — MiniMaxM2ReasoningParser broken for M2.5](https://github.com/vllm-project/vllm/issues/38212) — `extract_reasoning_streaming` assumes no opening `<think>` tag; M2.5/.7 do emit it.
- **[vLLM-24]** [Issue #34625 — Reasoning Parser not working with MiniMax M2.5](https://github.com/vllm-project/vllm/issues/34625) — same parser class affects M2.5+ reasoning extraction.
- **[vLLM-25]** [Issue #36778 — Thinking cannot be disabled on M2.5 via chat_template_kwargs](https://github.com/vllm-project/vllm/issues/36778) — drives strip-`enable_thinking` policy on the M2.7 route.
- **[vLLM-26]** [PR #35895 — minimax_m2 tool parser fix for stream_interval > 1](https://github.com/vllm-project/vllm/pull/35895) — covers one streaming malformation class.
- **[vLLM-27]** [Issue #36632 — MiniMax-M2.5 reasoning missing in chat completions stream](https://github.com/vllm-project/vllm/issues/36632) — `--reasoning-parser minimax_m2_append_think` skips reasoning content in SSE deltas.
- **[vLLM-28]** [Issue #28963 — MiniMax tool parsing errors](https://github.com/vllm-project/vllm/issues/28963) — error in `_convert_param_value`.

## Moonshot

- **[Moonshot-1]** [Kimi Chat Completion API reference](https://platform.kimi.ai/docs/api/chat) — wire schema for Moonshot-specific fields (`cache_key`, `prompt_cache_key`, `thinking`, `safety_identifier`).
- **[Moonshot-2]** [Kimi K2.6 quickstart](https://platform.kimi.ai/docs/guide/kimi-k2-6-quickstart) — K2.6-specific capabilities including multimodal content parts.
- **[Moonshot-3]** [Kimi-K2-Thinking tool_call_guidance.md](https://huggingface.co/moonshotai/Kimi-K2-Thinking/blob/main/docs/tool_call_guidance.md) — official guidance on client-side tool-call ID rewrite to canonical `functions.<name>:<global_idx>` form.
- **[Moonshot-4]** [OpenAI → Kimi API migration guide](https://kimi-ai.chat/guide/openai-to-kimi-api/) — Moonshot's own migration guide; shows `extra_body={"thinking":{"type":"disabled"}}` example that is misinterpreted as a wire field.

## Qwen

- **[Qwen-1]** [Qwen3-235B-A22B-Instruct-2507 model card](https://huggingface.co/Qwen/Qwen3-235B-A22B-Instruct-2507) — confirms model is non-thinking-only; basis for stripping `reasoning_effort`.
- **[Qwen-2]** [Qwen3-235B-A22B-Instruct-2507-FP8 model card](https://huggingface.co/Qwen/Qwen3-235B-A22B-Instruct-2507-FP8) — FP8 quantised variant model card; same non-thinking confirmation.
- **[Qwen-3]** [Qwen vLLM deployment docs](https://qwen.readthedocs.io/en/latest/deployment/vllm.html) — canonical placement for `enable_thinking` inside `chat_template_kwargs`; notes it is not OpenAI API compatible at the top level.

## MiniMax

- **[MiniMax-1]** [MiniMax platform docs index](https://platform.minimax.io/docs) — overview of MiniMax-hosted API surface.
- **[MiniMax-2]** [MiniMax-M2.7 model card](https://huggingface.co/MiniMaxAI/MiniMax-M2.7) — architecture (229B params, F32/BF16/F8_E4M3 tensor types), sampling recommendations, interleaved `<think>...</think>` constraint.
- **[MiniMax-3]** [MiniMax-M2.7 vLLM deployment guide](https://huggingface.co/MiniMaxAI/MiniMax-M2.7/blob/main/docs/vllm_deploy_guide.md) — exact CLI invocations, GPU sizing (220 GB weights + 240 GB/1M tokens), 196K max context per sequence, parser flags.
- **[MiniMax-4]** [MiniMax-M2.7 tool calling guide](https://huggingface.co/MiniMaxAI/MiniMax-M2.7/blob/main/docs/tool_calling_guide.md) — `<minimax:tool_call><invoke><parameter>` output format; `role:"tool"` with `content:[{name,type,text}]` array (no `tool_call_id`).
- **[MiniMax-5]** [MiniMax M2 Tool Use & Interleaved Thinking docs](https://platform.minimax.io/docs/guides/text-m2-function-call) — `extra_body.reasoning_split`, `reasoning_details[]` response field, multi-turn history rules.

## SGLang

- **[SGLang-1]** [Issue #23071 — MiniMax-M2 streaming tool_calls malformed](https://github.com/sgl-project/sglang/issues/23071) — under SSE streaming a single tool call sometimes splits into two entries (`name=null` + duplicated arguments).
- **[SGLang-2]** [Issue #16057 — MiniMax M2 tool parser failure on `str | null` union param types](https://github.com/sgl-project/sglang/issues/16057) — parser crashes on union-with-null in tool parameter schema; potential DoS vector via crafted tool schema.

## OpenRouter

- **[OpenRouter-1]** [Chat Completion API reference](https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request) — OpenRouter wire schema for `/api/v1/chat/completions`.
- **[OpenRouter-2]** [Plugins guide](https://openrouter.ai/docs/guides/features/plugins) — OpenRouter edge-only plugin invocation (`web`, `file-parser`, etc.).
- **[OpenRouter-3]** [Provider routing / selection](https://openrouter.ai/docs/guides/routing/provider-selection) — `provider` object schema (`order`/`only`/`ignore`/`quantizations`); OpenRouter-edge-only.
- **[OpenRouter-4]** [Reasoning tokens guide](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens) — `reasoning` object schema with `effort`/`max_tokens`/`exclude`/`enabled` sub-fields.
- **[OpenRouter-5]** [Unified parameters reference](https://openrouter.ai/docs/api/reference/parameters) — OpenRouter's unified parameter set across providers.
- **[OpenRouter-6]** [Web Search plugin](https://openrouter.ai/docs/guides/features/plugins/web-search) — `plugins.web` sub-spec; never executed downstream by vLLM.

## Security advisories

- **[CVE-1]** [CVE-2025-48887 / GHSA-w6q7-j642-7c25 (vLLM)](https://github.com/vllm-project/vllm/security/advisories/GHSA-w6q7-j642-7c25) — ReDoS in `pythonic` tool-call parser; engine must not run with `--tool-call-parser pythonic`.
- **[CVE-2]** [CVE-2025-48944 (NVD)](https://nvd.nist.gov/vuln/detail/CVE-2025-48944) — xgrammar crash on invalid `type` or pattern in JSON schema; drives `SchemaBounds` validators on `response_format` and `tools[].function.parameters`.
- **[CVE-3]** [CVE-2025-57809 / GHSA-5cmr-4px5-23pc (xgrammar)](https://github.com/mlc-ai/xgrammar/security/advisories/GHSA-5cmr-4px5-23pc) — xgrammar advisory; same crash class as CVE-2025-48944, applies to `structured_outputs.json` bounds.
- **[CVE-4]** [CVE-2025-58446 (GitLab advisory)](https://advisories.gitlab.com/pkg/pypi/xgrammar/CVE-2025-58446/) — `choice` entries: caps to ≤256 entries × 1 KiB each in `structured_outputs` validator.
- **[CVE-5]** [CVE-2025-61620 (NVD)](https://nvd.nist.gov/vuln/detail/CVE-2025-61620) — `chat_template_kwargs` Jinja injection; drives `ChatTemplateKwargsValidator` forbidden-key denylist.
- **[CVE-6]** [CVE-2025-62426 (NVD)](https://nvd.nist.gov/vuln/detail/CVE-2025-62426) — `tokenize=True` stalls the request handler; `tokenize` added to the same forbidden-key denylist.
- **[CVE-7]** [CVE-2025-9141 / GHSA-79j6-g2m3-jgfw (vLLM)](https://github.com/vllm-project/vllm/security/advisories/GHSA-79j6-g2m3-jgfw) — RCE via `eval()` in `qwen3_coder` tool-call parser; keep `hermes` parser on Qwen3-235B.
- **[CVE-8]** [CVE-2026-25048 / GHSA-7rgv-gqhr-fxg3 (xgrammar)](https://github.com/mlc-ai/xgrammar/security/advisories/GHSA-7rgv-gqhr-fxg3) — `grammar` field: drives ≤8 KiB + bracket-nesting depth ≤200 cap in `structured_outputs` validator.
- **[CVE-9]** [CVE-2026-34756 / GHSA-3mwp-wvh9-7528 (vLLM)](https://github.com/vllm-project/vllm/security/advisories/GHSA-3mwp-wvh9-7528) — unbounded `n` causes OOM; `CapUintParameterHandler` clamps `n ≤ 5`.
- **[CVE-10]** [CVE-2026-44222 / GHSA-hpv8-x276-m59f (vLLM)](https://github.com/vllm-project/vllm/security/advisories/GHSA-hpv8-x276-m59f) — special-token literals crash VL models; requires content sanitizer for Kimi-K2.6 multimodal path.
- **[CVE-11]** [CVE-2026-44223 / GHSA-83vm-p52w-f9pw (vLLM)](https://github.com/vllm-project/vllm/security/advisories/GHSA-83vm-p52w-f9pw) — penalty fields crash EngineCore with `extract_hidden_states` spec decode; pin vLLM ≥ 0.20.0.
- **[CVE-12]** [CVE-2026-27893 / RAXE-2026-044 (vLLM)](https://raxe.ai/labs/advisories/RAXE-2026-044) — vLLM hardcoded trust_remote_code bypass enables RCE via malicious model repositories; direct mitigation for the MiniMax-M2.7 route is the chain-pinned `HfCommit` SHA in the governance model config.
- **[CVE-13]** [CVE-2026-22778 (vLLM)](https://www.ox.security/blog/cve-2026-22778-vllm-rce-vulnerability/) — RCE via crafted video link in multimodal content; gateway mitigates by rejecting non-text content parts on all text-only routes (M2.7 included).
- **[CVE-14]** [CVE-2025-62164 / GHSA-mrw7-hf4f-83pf (vLLM)](https://github.com/advisories/GHSA-mrw7-hf4f-83pf) — tensor deserialization → DoS / potential RCE; pin vLLM ≥ patched release.

