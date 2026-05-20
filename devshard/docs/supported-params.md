> **⚠️ DEPRECATED**: The source of truth for the chat completions API contract has moved to [`docs/chat-api/`](../../docs/chat-api/README.md). Updates here will be discarded; this file will be deleted in a follow-up PR after content parity is reviewed.

# Chat Completions — supported parameters

Reference for `POST /v1/chat/completions` on devshard. Documents which OpenAI/vLLM request fields the gateway accepts today, which are rejected, how to use the ones that pass, and what we still need to tighten.

## Why a strict whitelist

vLLM crashes on malformed or pathological requests (deep recursive JSON Schema, unsupported routing fields, unbounded objects). To keep the inference node healthy:

1. **Body-level depth scan** (`ensureRequestNestingDepth` in `request_filters_parameters.go`). A byte-level pass bounds whole-request JSON nesting at `MaxRequestNestingDepth = 32` before `encoding/json` allocates anything. Rationale and the legitimate-request depth budget live in the code comment over the constant. This is a separate layer from the per-validator schema-depth caps below (`ResponseFormatMaxDepth = 16`, `ToolsMaxDepth = 16`, `StructuredOutputsMaxDepth = 16`): the body limit is generous because legitimate requests stack ~10 levels of wrappers; the schema-level limits are tight because the grammar compiler is the actual attack surface.
2. Every inbound `/chat/completions` body is then decoded into a generic JSON document.
3. `VLLMParameterCatalog` (`devshard/cmd/devshardctl/request_filters_parameters.go`) is a closed allow-list. The set of allowed keys is precomputed at catalog construction (no per-request map build). Any top-level field that is not in the catalog is rejected with `feature "<name>" is temporarily unavailable` (HTTP 400) before the request reaches the model.
4. Parameter rules run in two stages:
   - `PreValidation` — on the raw document, before we decode/validate it.
   - `PostLimits` — after `max_tokens` defaults/caps are resolved.
5. The message validator (`request_filters_messages.go`) enforces the OpenAI-compatible message contract (roles, tool_call linkage, text-only content parts).
6. The chat-request projection (`chatRequest` — 5 fields: `model`, `stream`, `max_tokens`, `max_completion_tokens`, `n`) is populated by direct map reads from the document, not a `json.Marshal + Unmarshal` round-trip.

Anything not on this whitelist does not reach the model. That is the contract.

## Status at a glance

### ✅ Implemented

**Schema-walking validators** — same vLLM grammar-compiler attack surface. All apply depth/nodes/size/branch/enum bounds, ban `$ref`/`$defs`/`definitions`, validate `type` is a JSON-Schema primitive, and compile-check `pattern` regex with a length cap (defuses CVE-2025-48944):
- `response_format` — `paramvalidators.ResponseFormatValidator`
- `tools[].function.parameters` — `paramvalidators.ToolsValidator`
- `structured_outputs.json` — `paramvalidators.StructuredOutputsValidator` (same xgrammar codepath as `response_format`; identical bounds. See dedicated subsection below.)
- `chat_template_kwargs` — `paramvalidators.ChatTemplateKwargsValidator` (plain object, no JSON-Schema semantics; additionally rejects keys that override `apply_hf_chat_template()` positional args: `chat_template`, `tokenize`, `tools`, `documents`, `conversation`, `continue_final_message`, `padding`, `truncation`, `max_length`, `return_tensors`, `return_dict` — defuses CVE-2025-61620 + CVE-2025-62426)

**Shape validators**:
- `thinking` — `paramvalidators.ThinkingValidator` (additionally mirrors the resolved boolean into `chat_template_kwargs.thinking` for `moonshotai/Kimi-K2.6`)
- `stream_options` — `paramvalidators.StreamOptionsValidator` (strip the entire field when `stream` is not exactly `true` per the [OpenAI Chat Completions reference](https://platform.openai.com/docs/api-reference/chat/create#chat_create-stream_options), since the field has no semantics outside streaming; otherwise whitelist `include_usage`, strip `continuous_usage_stats` for vLLM-project/vllm#9028 + any other sub-field; drop the field if it empties out)
- `metadata` — `paramvalidators.MetadataValidator` (OpenAI bounds: ≤16 keys × 64-char keys × 512-char string values)
- `user` — `paramvalidators.UserValidator` (must be a string, byte-length ≤ 512)
- `safety_identifier` — `paramvalidators.SafetyIdentifierValidator` (must be a string, byte-length ≤ 512 — gateway-chosen cap mirroring `UserValidator`; OpenAI's [help-center page](https://help.openai.com/en/articles/5428082-how-to-incorporate-a-safety-identifier) recommends short hashed identifiers but does not enforce a hard cap on their side). Per-model gated via `ModelScopedParameterHandler`: forwarded for `moonshotai/Kimi-K2.6` (Moonshot consumes the field for abuse tracking on their hosted backend), silently stripped for every other model. OpenAI is migrating end-user attribution from `user` to `safety_identifier` (same help-center page), so accepting the field keeps the gateway forward-compatible.
- `reasoning_effort` — `paramvalidators.ReasoningEffortValidator` (enum: `none|minimal|low|medium|high|xhigh`). The enum is sourced from [vLLM `ChatCompletionRequest.reasoning_effort`](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/chat_completion/protocol.py) where the docstring states these values come from OpenAI's API specification (vLLM additionally accepts `"max"` as a DeepSeek V4 extension — we exclude it because no routed model is DeepSeek). The classic OpenAI reasoning concept is documented in the [OpenAI reasoning guide](https://developers.openai.com/api/docs/guides/reasoning). After enum validation the field is stripped via `ModelScopedParameterHandler{Models: nil}` — every currently-routed model is non-reasoning, so forwarding is a documented no-op on both backends ([Qwen3-235B-Instruct-2507 model card](https://huggingface.co/Qwen/Qwen3-235B-A22B-Instruct-2507) — non-thinking-only; [Moonshot Kimi API](https://platform.kimi.ai/docs/api/chat) — schema lacks the field). Strip wiring must be revisited the moment a reasoning-capable model is added.
- `reasoning` — `paramvalidators.ReasoningValidator` (extracts `effort` from the [OpenRouter unified reasoning object](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens) and projects it onto top-level `reasoning_effort`; `enabled: false` is treated as an explicit opt-out and overrides any `effort`; `max_tokens` / `exclude` / `enabled: true` are dropped because they have no documented sink on our non-reasoning routes — `max_tokens` would map to Anthropic `budget_tokens` / Qwen `thinking_budget` / Gemini `thinkingBudget`, none of which apply, and `exclude` controls presentation of reasoning content the models don't emit). Wrapper is always removed after lift; top-level `reasoning_effort` wins on conflict.
- `enable_thinking` — `paramvalidators.EnableThinkingValidator` (bool type-check, then translates top-level `enable_thinking` into `chat_template_kwargs.enable_thinking` — the canonical Qwen3 placement per [Qwen vLLM deployment docs](https://qwen.readthedocs.io/en/latest/deployment/vllm.html): *"passing enable_thinking is not OpenAI API compatible"*). Pre-existing `chat_template_kwargs.enable_thinking` wins. For Qwen3-235B-Instruct-2507 the flag is a documented no-op (see model card above); the translation stays harmless and forward-compatible for future Qwen3-Thinking variants.
- `tool_choice` — `paramvalidators.ToolChoiceValidator` (shape-only: `"auto"` / `"none"` / function-object with ≤64 B name)

**`structured_outputs` — vLLM-extension constrained-decoding envelope**:

`structured_outputs` is a top-level field declared in [vLLM `sampling_params.py.StructuredOutputsParams`](https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py). It is **not** part of the OpenAI Chat Completions API ([OpenAI reference](https://platform.openai.com/docs/api-reference/chat/create) lists only `response_format`), nor part of [OpenRouter's unified parameters](https://openrouter.ai/docs/api/reference/parameters), nor part of [Moonshot's Kimi API](https://platform.kimi.ai/docs/api/chat). It IS the canonical way to drive vLLM's xgrammar / guidance / outlines backends from the OpenAI-compatible endpoint, and is exposed by `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` on our local vLLM. We accept it (validated) and forward to vLLM-served routes; we reject it on the Kimi K2.6 route with an explicit 400 since Moonshot does not consume it.

The envelope is an object with mutually-exclusive constraint fields (exactly one of `json` / `regex` / `choice` / `grammar` / `json_object` / `structural_tag` must be set, per the `__post_init__` rule at [sampling_params.py:59-80](https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py)) plus several option flags. The gateway enforces the same one-of rule before vLLM sees the request so misuse surfaces as 400 instead of 500.

Validation rules per sub-field (every cap is CVE-driven; same xgrammar codepath as `response_format`):

| Sub-field | Type | Rule | Source |
|---|---|---|---|
| `json` | object | Object only. The wire form `json: string` is upstream-legal per [vLLM `sampling_params.py:43`](https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py) (`json: str \| dict \| None`) but rejected at the gateway — the schema must be walked anyway, so requiring clients to send the parsed form avoids a second parse layer and a duplicate length cap. Full schema walk: `MaxDepth=16`, `MaxNodes=128`, `MaxSize=16 KiB`, `MaxBranch=16`, `MaxEnum=256`. Pattern fields: `MaxPatternLen=512` + Go-side `regexp.Compile` probe. `$ref`/`$defs`/`definitions` banned. `type` must be a JSON-Schema primitive (`string`/`number`/`integer`/`object`/`boolean`/`array`/`null` or an array of those). Identical to `ResponseFormatValidator` because xgrammar compiles both through the same path. | [CVE-2025-48944](https://nvd.nist.gov/vuln/detail/cve-2025-48944), [GHSA-vrq3-r879-7m65](https://github.com/vllm-project/vllm/security/advisories/GHSA-vrq3-r879-7m65) (response_format pattern/type DoS — same codepath); [CVE-2025-57809 / GHSA-5cmr-4px5-23pc](https://github.com/mlc-ai/xgrammar/security/advisories/GHSA-5cmr-4px5-23pc) (xgrammar infinite recursion on circular schemas — mitigated by depth cap and `$ref` ban) |
| `regex` | string | `len ≤ 512`, must compile under Go's `regexp` package. Defense-in-depth: even though xgrammar/guidance/outlines use Rust regex (linear-time per [vLLM docs](https://docs.vllm.ai/en/latest/features/structured_outputs.html)), the LM-format-enforcer fallback uses Python's `re` which IS vulnerable to catastrophic backtracking. The Go compile probe rejects obvious bombs uniformly. | [CVE-2025-48944](https://nvd.nist.gov/vuln/detail/cve-2025-48944) |
| `choice` | string array | `len(choice) ≤ 256`, each element `len ≤ 1 KiB`, total `sum(len) ≤ 16 KiB`. Matches `MaxEnum` and `MaxSize` of `response_format` because xgrammar lowers `choice` into the same enum-alternation grammar. | [CVE-2025-58446 / GHSA-9q5r-wfvf-rr7f](https://advisories.gitlab.com/pkg/pypi/xgrammar/CVE-2025-58446/) (xgrammar enum/grammar >100k chars causes minutes-long optimizer time) |
| `grammar` | string (EBNF) | `len ≤ 8 KiB`, active bracket-nesting depth ≤ 200 (opens `(`/`[`/`{` push, matching closes pop). Quoted-string contents are also counted — a legitimate EBNF terminal cannot embed >200 consecutive unbalanced opens as a literal. The xgrammar EBNF parser segfaulted before 0.1.32 on `'(' × 30000`; the upstream fix capped nesting at 1000 internally, we use 200 as a defensive margin. | [CVE-2026-25048 / GHSA-7rgv-gqhr-fxg3](https://github.com/mlc-ai/xgrammar/security/advisories/GHSA-7rgv-gqhr-fxg3) ([CVEReports detail](https://cvereports.com/reports/CVE-2026-25048)) |
| `json_object` | bool | Type-check only. Pure-passthrough flag asking vLLM for free-form JSON output. No attack surface. | [vLLM structured outputs docs](https://docs.vllm.ai/en/latest/features/structured_outputs.html) |
| `structural_tag` | string | `len ≤ 4 KiB`. This is the newest sub-feature ([vLLM structured outputs docs](https://docs.vllm.ai/en/latest/features/structured_outputs.html)) with no public CVE history as of audit date. Conservative byte cap; revisit when more audit data exists. | uncited — defensive default pending audit history |
| `whitespace_pattern` | string | Treated as a regex: `len ≤ 512` + `regexp.Compile` probe. Same exact rule as top-level `regex` and as schema-level `pattern`. | [CVE-2025-48944](https://nvd.nist.gov/vuln/detail/cve-2025-48944) |
| `disable_any_whitespace` | bool | Type-check only. Pure flag. | sampling_params.py |
| `disable_additional_properties` | bool | Type-check only. Pure flag. | sampling_params.py |
| `_backend`, `_backend_was_auto` | — | **Silently stripped** if present on the wire. These are documented as private internal fields (`sampling_params.py:55-57` lists them as `field(default=None, init=False)`), client code should never set them. | [sampling_params.py](https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py) |

Cross-field rules:
- **Exactly-one constraint:** of `{json, regex, choice, grammar, json_object, structural_tag}`, exactly one must be set. Both 0 and >1 trigger 400. Wire-explicit `null` is treated as absent (mirrors vLLM's `__post_init__` which checks `is not None`, [sampling_params.py:60-69](https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py)) — `{"json": null, "regex": "\\d+"}` has count 1 and is accepted. Gateway runs the count check before vLLM sees the request so 400 fires before vLLM 500s.
- **Closed allow-list inside the envelope:** only the documented sub-fields (`json`/`regex`/`choice`/`grammar`/`json_object`/`structural_tag` plus `whitespace_pattern`/`disable_any_whitespace`/`disable_additional_properties`) and the silent-stripped private fields (`_backend`/`_backend_was_auto`) are accepted. Any other key returns 400 — keeps the gateway's whitelist contract inside the envelope so unknown wire keys don't reach vLLM (vLLM's `StructuredOutputsParams` is a frozen dataclass — unexpected kwargs would surface as a 500 instead).
- **Conflict with `response_format`:** if both fields are set on the same request, gateway returns 400. vLLM's behavior in this case is not documented (the public docs do not state precedence); defensive rejection lets the gateway own the semantics rather than depend on vLLM internals.
- **Per-route gating:** the validator carries a `RejectedModels = [kimiK26ModelID]` list; when the route matches, it rejects with explicit 400 (`structured_outputs is not supported on this model — use response_format instead`). Same pattern as `ThinkingValidator.MirrorToTemplateKwargsForModels` — route gating inside the validator rather than a `ModelScopedParameterHandler` wrapper, since the catalog has no "always-reject" handler type today. The Moonshot Kimi API does not declare the field in its [chat API reference](https://platform.kimi.ai/docs/api/chat); forwarding it would either trip Moonshot's strict 400 or be silently dropped (Moonshot strict-mode behavior uncited). Explicit gateway rejection makes the contract visible.

Defense-in-depth notes:
- **Worker crashes despite valid input.** [vllm-project/vllm#24107](https://github.com/vllm-project/vllm/issues/24107) shows even a 3-property schema can crash xgrammar via CUDA illegal memory access on certain configs. Input validation alone is necessary but not sufficient — the gateway's per-request timeout + circuit-breaker is the second line of defense.
- **Schema cache pressure (CVE-2025-32381).** [xgrammar GHSA-389x-67px-mjg3](https://github.com/mlc-ai/xgrammar/security/advisories/GHSA-389x-67px-mjg3) (fixed in xgrammar 0.1.18): many requests with unique schemas fill an unbounded compile cache. Gateway-level mitigation: `MaxSize=16 KiB` per-request limits cache pressure, and any vLLM image we ship for Qwen3 must use xgrammar ≥ 0.1.32 (clears 32381 + 57809 + 58446 + 25048 in one bump).
- **Worker hang on huge schemas.** [vllm-project/vllm#14151](https://github.com/vllm-project/vllm/issues/14151) reports >1000-line schemas spinning ThreadPoolExecutor threads with no cancellation on client disconnect. `MaxSize=16 KiB` keeps us comfortably under the regression range.
- **FSM bypass in output.** [vllm-project/vllm#23120](https://github.com/vllm-project/vllm/issues/23120) (GPT-OSS): the model can emit plausibly-shaped JSON that violates the schema when non-final channel messages bypass the FSM. Downstream consumers must re-parse and re-validate; gateway-level shape enforcement does not guarantee the contract end-to-end.

**Length / size caps**:
- `messages` (≤2048) · `stop` (≤16/256B) · `stop_token_ids` (≤64) · `bad_words` (≤64/128B) · `logit_bias` map (≤1024 entries)

**Numeric range sanitizers**:
- `temperature` (≤2.0) · `top_p` · `top_k` · `min_p` · `repetition_penalty` (≤2.0) · `n` (≤5) · `logit_bias` values (`[-100, 100]`) · `frequency_penalty` (`[-2, 2]`) · `presence_penalty` (`[-2, 2]`)

**Type validators**:
- `seed` — non-negative uint64
- `tool_choice` — `paramvalidators.ToolChoiceValidator` (shape-only after upstream coerce). Accepts `"auto"`, `"none"`, or `{"type":"function","function":{"name":"..."}}` (name ≤ 64 B). `"required"` is silently coerced to `"auto"` by `ToolsValidator` (temporarily disabled — see Coercions). Anything else gets a 400 with the shape error.
- `thinking` — `paramvalidators.ThinkingValidator`. Accepts `{"type":"enabled"|"disabled"|"adaptive"|"auto"}`; `adaptive` and `auto` are client-side opt-in extensions emitted by Claude Code CLI / Kimi clients (the SDK is meant to resolve them into a concrete budget before the HTTP call — [Anthropic extended thinking docs](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking) enumerate only `enabled`/`disabled` on the wire, [Moonshot Kimi API](https://platform.kimi.ai/docs/api/chat) likewise). Both resolve to enabled — the client explicitly opted INTO thinking and just asked the SDK to choose the budget. **For `moonshotai/Kimi-K2.6` the mirror is the only thing that matters: the resolved boolean is written to `chat_template_kwargs.thinking` and the top-level `thinking` field is dropped entirely** — the kimi_k2 chat template only reads from `chat_template_kwargs`, top-level `thinking` has no vLLM-side consumer and would be dead weight on the wire. Existing `chat_template_kwargs.thinking` wins on conflict. For non-mirror routes (current: Qwen3-Instruct, where `thinking` is a no-op anyway) the top-level field is kept but the `type` is normalized to the canonical enum (`enabled`/`disabled`) so vLLM never sees `adaptive`/`auto`, and the sibling `display` field (Claude Code UI hint, not part of any provider contract) is silent-stripped.

**Coercions (no error, silent rewrite)**:
- `tool_choice` defaults to `"auto"` when `tools` is non-empty and the field is omitted (matches the OpenAI spec). Done by `ToolsValidator`.
- `tool_choice == "required"` is silently coerced to `"auto"` ("required" is temporarily disabled by network policy — re-enable by removing the coerce in `ToolsValidator.Validate`).
- `n` is coerced to `1` when `temperature == 0` — greedy sampling produces identical completions; vLLM rejects `n > 1` here, so we round it down silently instead of 400-ing
- **Kimi K2.6 only**: `frequency_penalty` and `presence_penalty` are force-rewritten to `0.0` via a `ModelScopedParameterHandler` catalog rule — Moonshot's K2.6 wire accepts the fields but rejects any non-zero value (model-side constraint, not security). Rewriting silently keeps OpenAI-clients that always emit these working. Other models (e.g. Qwen3-235B) get the catalog-level `[-2, 2]` clamp only.
- `tools[].function.strict` is stripped by `ToolsValidator` per tool. vLLM accepts the field but does not honor it (schema enforcement on tool arguments only flows through `tool_choice="required"`, not via `function.strict`); the kimi_k2 and hermes tool parsers ignore it. Stripping preserves wire compatibility with OpenAI clients (LangChain, LlamaIndex, OpenAI SDK) without implying schema enforcement we cannot deliver. Restore the pass-through here if vLLM ever wires `function.strict` into xgrammar.
- **Silent-strip fields** — accepted by the catalog and deleted from the request before forwarding. No inference-side semantics on vLLM for any of these; rejecting outright broke widely-used clients while forwarding bare would imply the backend honors the field. Stripping at the gateway is the OpenAI-compatible no-op. Per-field rationale + official sources:
  - `service_tier` — OpenAI billing/latency tier routing, enum `auto`/`default`/`flex`/`priority` ([OpenAI Flex processing guide](https://platform.openai.com/docs/guides/flex-processing), [Priority processing guide](https://developers.openai.com/api/docs/guides/priority-processing)). vLLM has one queue; field not in [`ChatCompletionRequest`](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/protocol.py) schema, silently dropped by `extra='allow'` ([vLLM PR #10463](https://github.com/vllm-project/vllm/pull/10463)).
  - `store` — OpenAI Stored Completions opt-in for distillation/evals ([OpenAI Chat Completions API reference](https://platform.openai.com/docs/api-reference/chat/create)). vLLM does not persist completions, so the field is a no-op regardless of value; forwarding it would create phantom-retention expectations for GDPR/audit pipelines.
  - `provider` — OpenRouter cross-provider routing object (`order`/`only`/`ignore`/`quantizations`/...) ([OpenRouter Provider Routing](https://openrouter.ai/docs/guides/routing/provider-selection)). OpenRouter-edge-only; meaningless on a single-backend vLLM path.
  - `plugins` — OpenRouter edge-only plugin invocation (`web` search, `file-parser`, etc.) ([OpenRouter Plugins](https://openrouter.ai/docs/guides/features/plugins), [Web Search plugin](https://openrouter.ai/docs/guides/features/plugins/web-search)); never executed downstream by vLLM.
  - `prompt_cache_key` — first-class OpenAI Chat Completions field for prompt-cache routing/sharding hints ([OpenAI Chat Completions API reference](https://platform.openai.com/docs/api-reference/chat/create)); also documented as Moonshot Kimi's required context-cache tag for Kimi Code Plan tier ([Moonshot Chat Completion API](https://platform.kimi.ai/docs/api/chat)). The vLLM-served path does NOT honor it: vLLM uses a separate field name `cache_salt` for prompt-cache isolation ([vLLM RFC #16016 — Cache Salting](https://github.com/vllm-project/vllm/issues/16016), shipped via [PR #17045](https://github.com/vllm-project/vllm/pull/17045)), and a request to alias `prompt_cache_key` → `cache_salt` is open since Jan 2026 with no merged PR ([Issue #33264](https://github.com/vllm-project/vllm/issues/33264)). Forwarding bare would give clients false cache-isolation guarantees in a domain with [published prompt-cache timing side-channel attacks](https://arxiv.org/abs/2502.07776) ([NDSS 2025 PROMPTPEEK](https://www.ndss-symposium.org/wp-content/uploads/2025-1772-paper.pdf)). Restore as a real feature (hash → inject as `cache_salt`) only when multi-tenant cache isolation is in scope; for now `cache_salt` is a pure vLLM extension that no mainstream agent SDK (LangChain, LlamaIndex, LiteLLM, OpenAI Python/Node) emits — so there is no client demand to bridge.
  - `cache_key` — Moonshot Kimi's native top-level context-cache hint, documented for the Kimi Code Plan tier ([Moonshot Chat Completion API](https://platform.kimi.ai/docs/api/chat)). Emitted in the wild by Moonshot's own `kimi-cli` (observed values like `cache_key: "kimi-cli_<hash>"`), which forwards the field even when the target is a non-Moonshot model. The Moonshot-hosted API consumes it at their proxy layer; **our path serves the same Kimi-K2.6 weights via vLLM, which does not honor `cache_key`** — same situation as `prompt_cache_key` above (vLLM uses `cache_salt` and the aliasing PR for either name is unmerged — [vLLM Issue #33264](https://github.com/vllm-project/vllm/issues/33264)). Silent-strip is the OpenAI-compatible no-op and avoids implying cache-isolation guarantees we cannot deliver. Bridge to `cache_salt` only when the same multi-tenant isolation design lands for `prompt_cache_key` — both share one rationale and should restore together.
  - `extra_body` — OpenAI Python SDK convention for inlining additional top-level fields into the request body. Per the SDK README's "Undocumented request params" section, `extra_body` is meant to be FLATTENED client-side into the JSON body before the HTTP call ([openai-python README: "Undocumented request params"](https://github.com/openai/openai-python/blob/main/README.md#undocumented-request-params)). A literal `extra_body` field on the wire indicates either a non-flattening client (e.g. some LiteLLM passthrough configs — [LiteLLM #4769](https://github.com/BerriAI/litellm/issues/4769)) or hand-written code that copied the SDK construct verbatim into a raw HTTP body (e.g. the example in the [Kimi OpenAI → Kimi API migration guide](https://kimi-ai.chat/guide/openai-to-kimi-api/), which shows `extra_body={"thinking":{"type":"disabled"}}` in Python and is easy to misinterpret as a wire field). We **unwrap** rather than strip: a pre-pass in `VLLMParameterCatalog.Apply` lifts each key from `extra_body` to the top level of the document **before** `rejectUnknownParameters` runs. Lifted keys then pass through the catalog's normal validation — known fields (e.g. `thinking`) reach their validators, unknown fields surface as the standard 400. Top-level keys always win on conflict (no silent overwrite); nested `extra_body` inside `extra_body` is not lifted (no recursion / smuggling); non-object envelopes (`extra_body: "x"`, `null`, `[]`, `42`) are silently dropped. The OpenAI Node SDK does not use the `extra_body` convention at all — its README documents request-level `body`/`headers`/`query` options on `client.post()`.
  - `extra_headers` — OpenAI Python SDK convention for HTTP-level header injection, paired with `extra_body` / `extra_query` in the same "Undocumented request params" section ([openai-python README](https://github.com/openai/openai-python/blob/main/README.md#undocumented-request-params)). Not a body field under correct SDK usage; should never reach our body validator at all. Strip-if-present is a defensive no-op for clients that accidentally serialize it into the body.
  - `thinking_config` — Google Gemini's native reasoning-control shape (`thinkingConfig: {thinkingBudget, includeThoughts}`, camelCase, nested under `generationConfig` per Google's API). Not in [OpenAI Chat Completions](https://platform.openai.com/docs/api-reference/chat/create), not in [OpenRouter unified parameters](https://openrouter.ai/docs/api/reference/parameters), not in [vLLM `ChatCompletionRequest` schema](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/chat_completion/protocol.py), not in [Moonshot Kimi API](https://platform.kimi.ai/docs/api/chat). Silent-strip is the lowest-friction option: clients that mistakenly forward a Gemini snippet to our endpoint don't break, and there's nothing meaningful to map to on Kimi/Qwen3-Instruct.
  - `think` — Ollama-style top-level reasoning flag ([Ollama thinking blog](https://ollama.com/blog/thinking)). Emitted by Cline / Ollama-CLI-compatible clients that target multiple backends. No vLLM-served route on the gateway today is reasoning-capable, so silent-strip mirrors `thinking_config` / `reasoning_effort` (validated-then-stripped). Revisit when a reasoning-capable route is added — `think:true` should then translate to the same sink as `enable_thinking` / `thinking`.
- `safety_identifier` follows the same "strip elsewhere" pattern but is forwarded for `moonshotai/Kimi-K2.6` (see Shape validators).

**Pipeline / forced**:
- `max_tokens` / `max_completion_tokens` — defaults + caps via `applyOutputTokenLimits`
- `min_tokens` — conditional strip + clamp
- `thinking_token_budget` — per-model gated via `ModelScopedParameterHandler` at PreValidation: silently stripped for every non-Kimi model (no documented sink; observed to crash `vllm.v1.engine.exceptions.EngineDeadError` on Qwen3-235B-Instruct-2507 when forwarded). For `moonshotai/Kimi-K2.6`: `paramvalidators.ThinkingTokenBudgetDefaultsValidator` injects `max_tokens / 2` when the client omits the field; `CapUintParameterHandler{Max: 96_000}` and `ClampUintToFieldParameterHandler{MaxField: "max_tokens"}` clamp the value (default-injected or client-supplied). Without the default, Kimi-K2.6 routinely consumes the entire `max_tokens` budget inside `<think>...</think>` and returns `finish_reason=length` with empty content at the current 4k cap. The 1:1 split self-balances at any size — a small `max_tokens` still leaves half for visible content (no floor, which would let reasoning eat the whole budget when `max_tokens` is tiny). Ceiling of 96 000 matches Moonshot's HLE/AIME reasoning budget.
- `logprobs` / `top_logprobs` — forced
- `messages` contract — `defaultChatMessageProcessor.ValidateDocument`
- Body-level: `MaxRequestNestingDepth=32`, `MaxChatRequestBodySize=10 MiB`

**Pass-through (safe by type contract)**:
- `model` · `stream` · `skip_special_tokens` · `detokenize` · `parallel_tool_calls`

### ❌ Open / not yet implemented

Field-level additions that Kimi K2/K2.6 actually accepts on the wire (per Moonshot's [chat reference](https://platform.kimi.ai/docs/api/chat) and [K2.6 quickstart](https://platform.kimi.ai/docs/guide/kimi-k2-6-quickstart)):

- **`messages[].reasoning_content`** — *required* on assistant turns during multi-step tool-calling when thinking is enabled. The message validator already does NOT reject unknown assistant-message fields (only `tool_call_id` is disallowed on assistant), so this already passes through. Just document the contract; no code change needed.

Structural additions:

- **Per-message content byte size cap** — currently bounded only by the 10 MiB body cap; no per-message structural limit. Token-limit logic downstream catches the most pathological cases.
- **Catalog → spec generator** — this doc is hand-written. `go generate` could derive the Supported table from `defaultVLLMParameterCatalog()` so it cannot drift.
- **Special-token literal sanitizer for `messages[].content`** (CVE-2026-44222) — literal strings like `<|vision_start|>` / `<|image_pad|>` / `<|vision_end|>` in user text crash multimodal models with an `IndexError` in `_vl_get_input_positions_tensor`. Required for `Kimi-K2.6` (multimodal) and any future VL routing. Implement as a content-text pass that strips or rejects the known special-token literals. ([advisory](https://github.com/vllm-project/vllm/security/advisories/GHSA-hpv8-x276-m59f))

Verification / operational:

These constraints are enforced by the vLLM engine configuration, not the gateway. The gateway cannot filter them — confirm on the serving side.

**Engine version**
- **Pin vLLM ≥ 0.20.0** — CVE-2026-44223 lets a single `repetition_penalty ≠ 1.0` (or any `frequency_penalty` / `presence_penalty`) crash EngineCore on vLLM 0.18.0–0.19.1 when `extract_hidden_states` speculative decoding is on. Confirm the deployed image's vLLM version before raising the cap on these fields. If the engine is older, the gateway must also reject `repetition_penalty ≠ 1.0` as a fallback. ([advisory](https://github.com/vllm-project/vllm/security/advisories/GHSA-83vm-p52w-f9pw))

**Tool-call parser selection (all served models)**
- **Disable `pythonic` tool-call parser** — CVE-2025-48887 catastrophic-backtracking ReDoS on the output path. The gateway can't block this; the engine must not run with `--tool-call-parser pythonic`. ([advisory](https://github.com/vllm-project/vllm/security/advisories/GHSA-w6q7-j642-7c25))

**Per-model operational requirements**

- **`Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`**
  - Tool-call parser must be `hermes` (the model's default). Do NOT run with `--tool-call-parser qwen3_coder` — CVE-2025-9141 yields RCE via `eval()` on attacker-controlled tool arguments. The `qwen3_coder` parser is intended for `Qwen3-Coder` only. ([advisory](https://github.com/vllm-project/vllm/security/advisories/GHSA-79j6-g2m3-jgfw))
  - Speculative decoding with `extract_hidden_states` must be off (see vLLM version note above) — otherwise `repetition_penalty` (currently allowed by the gateway) becomes a one-shot kill (CVE-2026-44223).
  - Reasoning parser `qwen3` and hermes tool parser are known to surface 500s on certain Qwen3 output patterns (multiple JSON blobs, `<tool_call>` inside `<think>`). These are server-side parsing bugs the gateway can't prevent — they degrade availability, not security. Track upstream issues (see References).

- **`moonshotai/Kimi-K2.6`** (multimodal model — text + image + video; gateway policy serves it as text-only)
  - Per [Moonshot K2.6 quickstart](https://platform.kimi.ai/docs/guide/kimi-k2-6-quickstart) the model natively accepts `{"type":"image_url"}` and `{"type":"video_url"}` content parts. The gateway message validator rejects non-text content parts (see "Messages contract" below), so image/video inputs never reach vLLM through this path.
  - **CVE-2026-44222 applies if vLLM is started with the multimodal pathway enabled** (which is the default for a VL/multimodal model). A user can still slip the literal string `<|vision_start|>` through text content, and the gateway does not sanitize it today. See `❌ Open / not yet implemented` above — a content sanitizer is required before we knowingly expose this attack surface.

### 🚫 Won't add (out of scope or by design)

- **`model` length cap** — redundant with 10 MiB body cap; minimal benefit on a routing-lookup field.
- **`tags`** — undocumented in any public chat-completions contract we serve (Hermes Agent's API doc explicitly says "standard OpenAI Chat Completions format" — see [hermes-agent api-server.md](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/api-server.md); OpenRouter uses structured `metadata` instead — [OpenRouter Chat Completion reference](https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request)). Accepting a folk-convention field with no upstream specification means codifying a contract we can't reference. Reject so clients surface the bug cleanly.
- **Provider-specific fields without vLLM semantics that bring no wire-compat value**: see "Unsupported parameters" below for the full categorized list. (Note: `user`, `metadata`, `parallel_tool_calls`, `stream_options`, `safety_identifier`, `service_tier`, `store`, `provider`, `plugins`, `prompt_cache_key`, `cache_key`, `extra_headers`, `thinking_config` are all in the Supported table above — silently stripped where the upstream has no semantics. `extra_body` is **unwrapped** rather than stripped. `reasoning_effort` is enum-validated then stripped (no current model is reasoning-capable). `reasoning` is translated into `reasoning_effort` (then stripped). `enable_thinking` is translated into `chat_template_kwargs.enable_thinking`. All entries have citations in Coercions / Shape validators above.)
- **vLLM-native legacy structured-output fields** (`guided_json`, `guided_regex`, `guided_grammar`, `guided_choice`, `enforced_tokens`) — superseded by `structured_outputs` upstream ([vLLM docs](https://docs.vllm.ai/en/latest/features/structured_outputs.html)). Clients should migrate; accepting both would double the validator surface.

## Supported parameters

| Field | Category | Stage / rule | Behavior |
| --- | --- | --- | --- |
| `model` | string | — | Required. Pass-through. Selects the upstream model id. |
| `stream` | bool | — | Pass-through. Streaming is enabled when `true`. |
| `messages` | object array | message validator + PreValidation length cap (`LengthCapListParameterHandler`) | Required, non-empty. `len(messages) ≤ 2048` (way above any realistic conversation; defense against JSON-parse memory amplification, independent of token count). Strict OpenAI-compatible message contract — see "Messages" section below. |
| `max_tokens` | int range | PostLimits (token-limit pipeline) | If absent → set to `DefaultRequestMaxTokens`. Capped at `RequestMaxTokensCap` unless the request carries admin auth. When both `max_tokens` and `max_completion_tokens` are set, the smaller wins and both are aligned. |
| `max_completion_tokens` | int range | PostLimits (token-limit pipeline) | Same rules as `max_tokens`. |
| `n` | int range | PostLimits sanitize (`CapUintParameterHandler`) + greedy coerce | Capped at `MaxChatRequestChoices` (5). Coerced to `1` when `temperature == 0` (vLLM rejects `n > 1` under greedy sampling). |
| `temperature` | float range | PostLimits sanitize | Strips `NaN`/`±Inf`. Capped at `MaxTemperature` (2.0). String-encoded numbers accepted. |
| `top_p` | float range | PostLimits sanitize | Strips `NaN`/`±Inf`. No upper cap (forwarded raw). |
| `top_k` | int range | PostLimits sanitize | Strips `NaN`/`±Inf`. vLLM-specific. |
| `min_p` | float range | PostLimits sanitize | Strips `NaN`/`±Inf`. vLLM-specific. |
| `repetition_penalty` | float range | PostLimits sanitize | Strips `NaN`/`±Inf`. Capped at `MaxRepetitionPenalty` (2.0). |
| `frequency_penalty` | float range | PostLimits sanitize | Strips `NaN`/`±Inf`. Clamped to `[-2.0, 2.0]`. **Kimi K2.6**: force-rewritten to `0.0` via a `ModelScopedParameterHandler` catalog rule (model accepts only `0.0` on the wire). |
| `presence_penalty` | float range | PostLimits sanitize | Strips `NaN`/`±Inf`. Clamped to `[-2.0, 2.0]`. **Kimi K2.6**: force-rewritten to `0.0` via a `ModelScopedParameterHandler` catalog rule (model accepts only `0.0` on the wire). |
| `logit_bias` | int→float map | PostLimits sanitize (`SanitizeFloatMapParameterHandler`) | Keeps numeric entries in `[-100, 100]`. Drops the field if the map is empty after sanitization. Map size capped at 1024 entries (rejected with HTTP 400 over the cap). |
| `stop` | string list | PreValidation length cap (`LengthCapListParameterHandler`) | Pass-through within bounds. Rejected if `len(stop) > 16` or any entry's string length > 256 bytes. |
| `stop_token_ids` | int list | PreValidation length cap (`LengthCapListParameterHandler`) | Pass-through within bounds. Rejected if `len(stop_token_ids) > 64`. |
| `seed` | int range | PreValidation type-check (`ValidateUintParameterHandler`) | Must parse as a non-negative integer that fits in uint64. Otherwise rejected with HTTP 400 at the gateway boundary instead of relying on the upstream's error path. |
| `skip_special_tokens` | bool | — | Pass-through. vLLM-specific. |
| `detokenize` | bool | — | Pass-through. vLLM-specific. |
| `thinking` | object | PreValidation validate (`paramvalidators.ThinkingValidator`) | Must be `{"type": "enabled" \| "disabled" \| "adaptive" \| "auto"}`. `adaptive` / `auto` are client-side opt-in extensions ([Anthropic extended thinking](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking) — wire enum only has `enabled`/`disabled`; both extensions resolve to enabled). Any other shape rejected. **For `moonshotai/Kimi-K2.6`** the resolved boolean is mirrored into `chat_template_kwargs.thinking` (existing value wins) and the top-level `thinking` field is **dropped entirely** — vLLM's kimi_k2 chat template only reads from `chat_template_kwargs`. For non-mirror routes the top-level field stays but `type` is normalized to the canonical enum (`enabled`/`disabled`) and the sibling `display` field (Claude Code UI hint) is silent-stripped. |
| `chat_template_kwargs` | object | PreValidation validate (`paramvalidators.ChatTemplateKwargsValidator`) | Pass-through within plain-object bounds: depth ≤ 5, nodes ≤ 128, serialized size ≤ 16 KiB. Top-level keys that override `apply_hf_chat_template()` positional arguments are rejected (`chat_template`, `tokenize`, `tools`, `documents`, `conversation`, `continue_final_message`, `padding`, `truncation`, `max_length`, `return_tensors`, `return_dict` — defuses CVE-2025-61620 + CVE-2025-62426). `add_generation_prompt` is explicitly *allowed* as a legitimate template knob. |
| `tool_choice` | string \| object | PreValidation validate | Accepted values: `"auto"`, `"none"`, or `{"type":"function","function":{"name":"..."}}` (name ≤ 64 B). `"required"` is silently coerced to `"auto"` by `ToolsValidator` (temporarily disabled). Other shapes (number, array, unknown string, malformed object) get a 400. Stripped together with `tools` if `tools` arrives empty. If `tools` is non-empty and `tool_choice` is omitted, gateway writes `"auto"` (matches the OpenAI spec). |
| `min_tokens` | int range | PreValidation conditional strip + PostLimits clamp | Dropped if `stop_token_ids` is also set (vLLM rejects the combination). Otherwise clamped to ≤ `max_tokens`. |
| `bad_words` | string list | PreValidation sanitize + length cap | Empty/whitespace strings are removed. Field is dropped if the resulting list is empty. Then `len(bad_words) ≤ 64`, per-entry length ≤ 128 bytes. |
| `tools` | object array | PreValidation sanitize + shape + schema validate (`paramvalidators.ToolsValidator`) | Empty arrays drop both `tools` and `tool_choice`. Each tool must declare `type: "function"` and a `function` object with a non-empty string `name` — the OpenAI tool contract — otherwise rejected before vLLM ever sees the request. If `function.parameters` is present, it is walked as a JSON Schema with bounds depth ≤ 16, nodes ≤ 256, branch arms ≤ 16, enum ≤ 256, size ≤ 16 KiB; `$ref`/`$defs`/`definitions` forbidden; `type` must be a JSON-Schema primitive; `pattern` must compile as a regex within 512 B — CVE-2025-48944. Parameter-less tools are valid. Both depth and node-count limits are wider than `response_format` (16/256 vs 5/128) because production agent tool schemas with nested presentation structures routinely sit at 5–13 levels with up to ~170 nodes; the wider tools bounds give headroom over observed max, while MaxSize=16 KiB, MaxBranch=16, and the `$ref`/`$defs` ban provide the actual schema-bomb defense. |
| `logprobs` | bool | PostLimits force | Forced to `true` for the gateway's observability pipeline regardless of what the client sent. |
| `top_logprobs` | int range | PostLimits force | Forced to `5` for the same reason. |
| `response_format` | object | PreValidation validate (`paramvalidators.ResponseFormatValidator`) | Accepted with bounds. `type` must be one of `text` / `json_object` / `json_schema`. For `json_schema`, the schema is walked once and rejected if depth > 16, nodes > 128, branch arms (anyOf/oneOf/allOf) > 16, enum > 256, serialized size > 16 KiB, or contains `$ref` / `$defs` / `definitions`. Every schema node's `type` must be a JSON-Schema primitive (`string`/`number`/`integer`/`object`/`boolean`/`array`/`null` or an array of those); `pattern` is byte-capped at 512 B and must compile as a regex (defuses CVE-2025-48944). |
| `structured_outputs` | object | PreValidation validate (`paramvalidators.StructuredOutputsValidator` with `RejectedModels = [kimiK26ModelID]`) | vLLM-native constrained-decoding envelope per [vLLM `sampling_params.py`](https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py). Closed allow-list inside the envelope: only the documented sub-fields are accepted, anything else returns 400. Exactly one of `json` / `regex` / `choice` / `grammar` / `json_object` / `structural_tag` must be set (mirrors `__post_init__` `count == 1` rule; wire-explicit `null` counts as absent). Per-sub-field bounds: `json` (object only — string form rejected; walks the same caps as `response_format`: depth ≤ 16, nodes ≤ 128, size ≤ 16 KiB, branch ≤ 16, enum ≤ 256, `pattern` ≤ 512 B + regex compile, ban `$ref`/`$defs`/`definitions`, primitive type whitelist — CVE-2025-48944 + CVE-2025-57809); `regex` and `whitespace_pattern` ≤ 512 B + regex compile (CVE-2025-48944); `choice` ≤ 256 entries × ≤ 1 KiB each, total ≤ 16 KiB (CVE-2025-58446); `grammar` ≤ 8 KiB + active bracket-nesting depth ≤ 200 (CVE-2026-25048); `structural_tag` ≤ 4 KiB (conservative — no public CVE yet, no public audit). Boolean flags (`json_object`, `disable_any_whitespace`, `disable_additional_properties`) type-checked only. Private internal fields (`_backend`, `_backend_was_auto`) stripped silently. Returns 400 if both `structured_outputs` and `response_format` are set (vLLM precedence is undocumented; defensive rejection). Rejected with 400 on the `moonshotai/Kimi-K2.6` route — Moonshot's API does not declare the field. See dedicated subsection above for the full CVE table. |
| `user` | string | PreValidation validate (`paramvalidators.UserValidator`) | OpenAI-standard end-user identifier for abuse tracking; no inference-side semantics on the vLLM upstream, ignored on the wire. Must be a string; byte-length ≤ 512 (covers production identifiers — `user_<random>`, UUIDs, hashed ids, email-shaped, framework session ids — while preventing the field from being used as a 10 MiB body-size carrier). |
| `metadata` | object | PreValidation validate (`paramvalidators.MetadataValidator`) | OpenAI-standard tracking object (LangSmith, distributed tracing, A/B tagging). Bounded to ≤16 keys × 64-char keys × 512-char string values — the same limits the OpenAI API itself enforces. Values must be strings; any other type rejected. |
| `parallel_tool_calls` | bool | — | Pass-through. OpenAI-standard tool-calling control. Some vLLM versions honor it, some ignore it — the client opted in to that behavioral divergence; no DoS surface. |
| `stream_options` | object | PreValidation validate (`paramvalidators.StreamOptionsValidator`) | Stripped entirely when `stream` is not exactly `true` (absent, `false`, or any non-bool value) — the [OpenAI reference](https://platform.openai.com/docs/api-reference/chat/create#chat_create-stream_options) states the field is *"only set this when you set `stream: true`"*; carrying it on a non-streaming request is client misuse. When streaming is on, sub-field whitelist applies: only `include_usage` survives (OpenAI-documented opt-in for a final-chunk `usage` object, required by streaming clients needing token accounting). `continuous_usage_stats` is stripped (triggers vLLM-project/vllm#9028: per-chunk usage counter is wrong under chunked prefill). Any other / future sub-field is also stripped. If the object empties out, the field is dropped entirely so it never reaches the upstream as `{}`. |
| `safety_identifier` | string | PreValidation gate (`ModelScopedParameterHandler` → `paramvalidators.SafetyIdentifierValidator`) | OpenAI is [migrating end-user attribution from `user` to `safety_identifier`](https://help.openai.com/en/articles/5428082-how-to-incorporate-a-safety-identifier). Forwarded to `moonshotai/Kimi-K2.6` (Moonshot consumes the field for abuse tracking on their hosted backend) after a string + ≤512 B shape check (gateway-chosen cap, mirroring `UserValidator`; OpenAI does not enforce a hard length). Silently stripped for every other model — no documented downstream consumer on the vLLM-served path. |
| `service_tier` | string | PreValidation strip | Silently stripped at the gateway. OpenAI billing/latency tier routing (`auto`/`default`/`flex`/`priority`); vLLM has a single queue and ignores the field. Accepting + stripping preserves OpenAI SDK wire-compat without implying tier semantics we cannot deliver. |
| `store` | bool | PreValidation strip | Silently stripped at the gateway. OpenAI Stored Completions opt-in; vLLM does not persist completions, so the field is a no-op. Forwarding would create a false retention expectation for eval/distillation pipelines; stripping makes the no-op behavior consistent and auditable. |
| `provider` | object | PreValidation strip | Silently stripped at the gateway. OpenRouter cross-provider routing object; meaningless on a single-backend vLLM path. Pure routing metadata (no URLs, no code paths consumed) so stripping is zero-risk and avoids 400s for OpenRouter-aware clients hitting our endpoint. |
| `plugins` | array | PreValidation strip | Silently stripped at the gateway. OpenRouter edge-only plugin invocation (`web` search, `file-parser`, etc.); never executed downstream on our path. Stripping avoids surfacing a misleading "supported" surface to clients while keeping wire-compat. |
| `prompt_cache_key` | string | PreValidation strip | Silently stripped at the gateway. First-class OpenAI Chat Completions field ([OpenAI API reference](https://platform.openai.com/docs/api-reference/chat/create)); also documented by Moonshot for Kimi Code Plan tier ([Moonshot Chat Completion API](https://platform.kimi.ai/docs/api/chat)). vLLM does NOT honor this field — it uses a separate `cache_salt` field for cache isolation ([vLLM RFC #16016](https://github.com/vllm-project/vllm/issues/16016) / [PR #17045](https://github.com/vllm-project/vllm/pull/17045)) and the aliasing request is still open ([vLLM Issue #33264](https://github.com/vllm-project/vllm/issues/33264)). Stripping prevents 400s for cache-aware clients without false isolation promises. Restore as a real feature only if hash → `cache_salt` injection becomes a requirement (note: `cache_salt` is a pure vLLM extension; no mainstream agent SDK emits it). |
| `cache_key` | string | PreValidation strip | Silently stripped at the gateway. Moonshot Kimi's native top-level context-cache hint ([Moonshot Chat Completion API](https://platform.kimi.ai/docs/api/chat)) — emitted in the wild by `kimi-cli` (Moonshot's own CLI), which forwards `cache_key: "kimi-cli_<hash>"` even when targeting non-Moonshot models on a proxy. Our path serves the same Kimi-K2.6 weights via vLLM, which does not honor `cache_key`; same upstream gap as `prompt_cache_key` ([vLLM Issue #33264](https://github.com/vllm-project/vllm/issues/33264) still unmerged). Silent-strip prevents 400s for cache-aware clients without implying isolation guarantees. Restore together with `prompt_cache_key` if/when a hash → `cache_salt` bridge lands. |
| `extra_body` | object | PreValidation unwrap (catalog pre-pass) | **Unwrapped** to the top level of the document before `rejectUnknownParameters` runs, so lifted keys flow through the catalog's normal validation. OpenAI Python SDK convention is to FLATTEN client-side ([openai-python README: "Undocumented request params"](https://github.com/openai/openai-python/blob/main/README.md#undocumented-request-params)); a literal `extra_body` field on the wire indicates a non-flattening client (e.g. LiteLLM passthrough — [LiteLLM #4769](https://github.com/BerriAI/litellm/issues/4769)) or hand-rolled code that copied the construct verbatim (e.g. the [Kimi migration guide](https://kimi-ai.chat/guide/openai-to-kimi-api/) example). Lift semantics: top-level keys win on conflict; nested `extra_body` is not lifted; non-object envelopes are silently dropped. Known lifted keys (e.g. `thinking`) reach their validators normally; unknown lifted keys surface as the standard unsupported-field 400. |
| `extra_headers` | object | PreValidation strip | Silently stripped at the gateway. OpenAI Python SDK HTTP-level header injection, documented alongside `extra_body` / `extra_query` ([openai-python README: "Undocumented request params"](https://github.com/openai/openai-python/blob/main/README.md#undocumented-request-params)); not a body field under correct SDK use. Strip-if-present is a defensive no-op for clients that accidentally serialize it into the body. |
| `reasoning_effort` | enum string | PreValidation validate (`paramvalidators.ReasoningEffortValidator`) + `ModelScopedParameterHandler` strip | Enum: `none|minimal|low|medium|high|xhigh` sourced from [vLLM `ChatCompletionRequest.reasoning_effort`](https://github.com/vllm-project/vllm/blob/main/vllm/entrypoints/openai/chat_completion/protocol.py) (vLLM also accepts `"max"` for DeepSeek V4 — excluded here); concept from the [OpenAI reasoning guide](https://developers.openai.com/api/docs/guides/reasoning). Validated then stripped — every currently-routed model is non-reasoning ([Qwen3-235B-Instruct-2507](https://huggingface.co/Qwen/Qwen3-235B-A22B-Instruct-2507) explicitly non-thinking; [Moonshot Kimi](https://platform.kimi.ai/docs/api/chat) schema lacks the field). Re-check strip wiring when adding a reasoning-capable model. |
| `reasoning` | object | PreValidation translate (`paramvalidators.ReasoningValidator`) | Extracts `effort` from the [OpenRouter unified reasoning object](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens) and lifts it onto top-level `reasoning_effort`. `enabled: false` is honored as an explicit opt-out (no lift). `max_tokens` / `exclude` / `enabled: true` are silent-dropped — no documented sink on our non-reasoning routes. Wrapper always removed after lift; top-level `reasoning_effort` wins on conflict. |
| `enable_thinking` | bool | PreValidation translate (`paramvalidators.EnableThinkingValidator`) | Translates top-level `enable_thinking` into `chat_template_kwargs.enable_thinking` per the canonical Qwen3 placement ([Qwen vLLM deployment docs](https://qwen.readthedocs.io/en/latest/deployment/vllm.html): *"passing enable_thinking is not OpenAI API compatible"*). Pre-existing `chat_template_kwargs.enable_thinking` wins. For Qwen3-235B-Instruct-2507 the flag is a documented no-op; harmless and forward-compatible for future Qwen3-Thinking variants. |
| `thinking_config` | object | PreValidation strip | Gemini-native (`thinkingConfig: {thinkingBudget, includeThoughts}` under `generationConfig`). Not in any OpenAI / OpenRouter / vLLM / Moonshot contract we serve — silent-strip lets misconfigured clients pass without breakage. |
| `think` | bool | PreValidation strip | Ollama-style top-level reasoning flag ([Ollama thinking blog](https://ollama.com/blog/thinking)). No reasoning-capable route on the gateway today, so silent-strip mirrors `thinking_config` / `reasoning_effort`. Revisit when a reasoning route is added — translate to the appropriate per-model sink at that point. |

### Messages contract (recap)

Enforced by `request_filters_messages.go`:

- Roles: `developer`, `system`, `user`, `assistant`, `tool`, `function`.
- Assistant `content` may be empty/null only when `tool_calls` or `function_call` is present.
- Tool messages require `tool_call_id` matching a prior assistant `tool_calls[].id`.
- Function messages require `name`.
- Content parts: only `{"type": "text", "text": "..."}` is accepted. Typed arrays of text parts are flattened to a single string before forwarding.
- Empty tool `content` is normalized to a sentinel string; missing tool `content` is also normalized.
- **Lenient SDK compat:** explicit JSON `null` for assistant `tool_calls` / `function_call` is treated as field-absent and the key is dropped before forwarding. OpenAI-Python and several LangChain-derived clients serialize empty slots as `null`; rejecting was a gateway-side false-positive (cf. captured-requests April 2026 batch).
- **Lenient SDK compat:** `name` on `role: "tool"` messages is silently stripped before validation. The field was required in the legacy `role: "function"` API; modern OpenAI Chat Completions documents only `role` / `content` / `tool_call_id` on tool messages ([OpenAI reference](https://platform.openai.com/docs/api-reference/chat/create#chat_create-messages)), and vLLM ignores extra keys. Stripping silently lets habitual emitters (LangChain templates, hand-rolled clients carrying the function-API shape) pass without a 400.
- **Lenient SDK compat:** empty-array `content: []` is treated the same as `null` / `""` — whitespace string, empty string, and empty array all normalize uniformly. For assistant messages with `tool_calls` / `function_call` this means a "tool-only response" (no prose) passes; for `tool` messages the empty content becomes a sentinel string. SDK bridges (notably Anthropic ↔ OpenAI translation layers) emit `[]` for tool-call-only assistant turns instead of `null`; rejecting was a gateway-side false-positive (cf. captured-requests April 2026 batch, req-1779259426225387173-15356).
- **Lenient SDK compat:** orphan `role: "tool"` messages — those whose `tool_call_id` was never emitted by a prior `assistant.tool_calls[].id` — are silently dropped before validation. Long agent conversations sometimes lose part of a multi-tool fan-out during client-side history compaction (Anthropic ↔ OpenAI bridges, session-resume frameworks) but keep all tool responses. Strict-spec rejection 400'd the whole request; silent drop preserves the conversation while the survivors (non-orphan tool responses) still pass the strict linkage check. Captured-requests April 2026 batch: 33 captures traced to this pattern (e.g. req-1779263614553842074-24798).
- **Lenient SDK compat:** empty `role: "assistant"` turns — no `content` (missing/null/empty-string/empty-array) AND no `tool_calls` AND no `function_call` — are silently dropped. Session-resume frameworks sometimes leave a literal `{"role":"assistant"}` placeholder when the prose response between a tool result and the next user turn gets lost during history compaction. The model can't observe an informationless turn anyway, so the drop is a semantic no-op. Captured-requests April 2026 batch: 2 captures (req-1779291749005921857-69735, req-1779291761840125274-69744).
- **Strict (no lenient compat):** duplicate `tool_calls[].id` within a single assistant message is rejected per [OpenAI Chat Completions spec](https://platform.openai.com/docs/api-reference/chat/create) — matches what every mainstream provider does (OpenAI server: HTTP 400 "Duplicate value for 'tool_call_id'"; Anthropic / Bedrock: `ValidationException: duplicate Ids`). Has been observed coming from a model-side emission bug in Kimi-K2.6's vLLM tool parser ([vLLM PR #21259 review thread](https://github.com/vllm-project/vllm/pull/21259) — `history_tool_call_cnt` recomputed inside the per-choice loop with `n>1` can collide on `functions.<name>:<idx>`). Captured-requests May 2026 batch confirms the agent has produced multiple distinct tool results for the duplicated id (req-1779369319274519506-325651: two `tool_call_id = "functions.ReadCommandOutput:2"` messages with different output content), so silent gateway-side dedup (drop / rename) would risk losing real results. Moonshot's official guidance ([Kimi-K2-Thinking tool_call_guidance.md](https://huggingface.co/moonshotai/Kimi-K2-Thinking/blob/main/docs/tool_call_guidance.md)) is **client-side ID rewrite to canonical `functions.<name>:<global_idx>` form** before round-tripping — not gateway-side compensation. Rejecting at the boundary preserves spec conformance, gives clients a clear signal to implement the documented rewrite workaround, and pushes back on the upstream emission bug.

## Unsupported parameters (rejected)

Every OpenAI / vLLM / provider-specific field that is **not** in the Supported table is rejected at `PreValidation`. The error response is HTTP 400 with body `feature "<name>" is temporarily unavailable`.

Grouped by why the field is off:

| Group | Examples we reject today | Origin | Why off |
| --- | --- | --- | --- |
| Structured-output (non-`response_format`) | `guided_json`, `guided_regex`, `guided_grammar`, `guided_choice`, `enforced_tokens` | vLLM-native | Legacy vLLM-native fields predating `structured_outputs`. The current `structured_outputs` envelope IS supported under `paramvalidators.StructuredOutputsValidator` (see Supported table). The legacy `guided_*` set is kept rejected — clients should migrate to `structured_outputs` instead per [vLLM docs](https://docs.vllm.ai/en/latest/features/structured_outputs.html); accepting both would require a second walker and double the validator surface. |
| Tool-calling extensions | (none — `parallel_tool_calls` is now passthrough; `tool_choice="required"` is silently coerced to `"auto"` by `ToolsValidator`, see Supported table) | OpenClaw, OpenAI | "required" is temporarily disabled by network policy (cost-amplifier + engine-wedge observed historically). |
| Routing / metadata | `tags`, `seed_override` | Hermes, OpenClaw, Kilo, Nous | `tags` is undocumented in any public chat-completions contract we serve ([hermes-agent api-server.md](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/api-server.md), [OpenRouter Chat Completion reference](https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request) — neither documents the field). Reject so misuse surfaces cleanly. (`user`, `metadata`, `service_tier`, `provider`, `plugins`, `extra_headers` moved to Supported as silent-strip / shape-validated entries; `extra_body` moved to Supported as the **unwrap** entry — see Supported table.) |
| Caching | — | — | (`prompt_cache_key` and `store` were moved to Supported as silent-strip — vLLM does not honor either, so stripping is the OpenAI-compatible no-op; see Supported table for citations.) |
| Reasoning (non-vLLM-native) | — | — | (`reasoning_effort`, `reasoning`, `enable_thinking`, `thinking_config` were moved to Supported — see Shape validators / Coercions above. `reasoning_effort` validates then strips; `reasoning` translates to `reasoning_effort`; `enable_thinking` translates to `chat_template_kwargs.enable_thinking`; `thinking_config` is silent-stripped.) |
| Provider-specific extras | `vl_high_resolution_images` (Qwen, when sent via `extra_body`), `messages[].reasoning_content` (Kimi multi-turn replay), `chat_template_kwargs.preserve_thinking` (Qwen wrapper) | Hermes, OpenClaw | Single-vendor attribution / wrapping fields. No vLLM equivalent. Now that `extra_body` is **unwrapped**, lifted inner keys are subject to the standard whitelist — `extra_body.vl_high_resolution_images`, `extra_body.tags` surface as 400 as if they were sent at the top level. (`extra_body.provider`, `extra_body.plugins`, `extra_body.thinking_config` are accepted because their top-level counterparts are silent-stripped on the supported list; `tools[].function.strict` was moved out — silently stripped by `ToolsValidator`, see Coercions.) |

If you see a field here that you need, add it to the catalog with the smallest safe rule — see "Validation improvements".

## How to use the gateway

### Minimal request

```http
POST /v1/chat/completions
Content-Type: application/json

{
  "model": "moonshotai/Kimi-K2.6",
  "messages": [{"role": "user", "content": "Hello"}]
}
```

`max_tokens` is filled in for you (`DefaultRequestMaxTokens`).

### With sampling knobs

```json
{
  "model": "moonshotai/Kimi-K2.6",
  "messages": [{"role": "user", "content": "Hello"}],
  "temperature": 0.7,
  "top_p": 0.95,
  "top_k": 40,
  "repetition_penalty": 1.05,
  "max_tokens": 512
}
```

All five knobs are sanitized at `PostLimits`: non-finite numbers and out-of-range values are dropped or clamped before the request reaches vLLM.

### With tools (function calling)

```json
{
  "model": "moonshotai/Kimi-K2.6",
  "messages": [{"role": "user", "content": "Weather in Berlin?"}],
  "tools": [
    {"type": "function", "function": {"name": "get_weather",
      "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}
  ],
  "tool_choice": "auto"
}
```

If `tools` is `[]`, the gateway strips both `tools` and `tool_choice` (the vLLM build we run rejects empty tools).

If `tools` is non-empty but `tool_choice` is omitted, the gateway writes `tool_choice: "auto"` before the request reaches vLLM. This matches OpenAI's documented default and avoids a class of 400s seen in production when clients drop the field and the backend wasn't started with `--enable-auto-tool-choice`.

If `temperature == 0` and `n > 1`, the gateway rewrites `n` to `1` (greedy sampling produces identical completions, and vLLM otherwise rejects the request). Clients receive the response without a 400 and without retries.

### With Kimi-K2.6 thinking

```json
{
  "model": "moonshotai/Kimi-K2.6",
  "messages": [{"role": "user", "content": "Plan this refactor"}],
  "thinking": {"type": "enabled"}
}
```

For `moonshotai/Kimi-K2.6` the gateway also writes `chat_template_kwargs.thinking = true` automatically.

### Rejected request

```json
{
  "model": "moonshotai/Kimi-K2.6",
  "messages": [{"role": "user", "content": "Hello"}],
  "response_format": {"type": "json_object"}
}
```

→ `HTTP 400 — feature "response_format" is temporarily unavailable`. Same shape for any other unknown key.

## Design notes

### `response_format` invariants (reference)

The motivating attack that drove the validator design — a JSON Schema with ~200 nested `properties` levels would crash vLLM:

```json
{
  "response_format": {
    "type": "json_schema",
    "json_schema": {
      "name": "r",
      "schema": {"properties": {"x": {"properties": {"x": {"properties": {"x": "..."}}}}}, "required": ["x"], "type": "object"}
    }
  }
}
```

A safe `response_format` handler must enforce all of the following at `PreValidation` and reject the request with HTTP 400 if any check fails:

| Invariant | Initial threshold | Rationale |
| --- | --- | --- |
| `type` ∈ {`text`, `json_object`, `json_schema`} | Hard whitelist | Same set OpenClaw/Kilo document. Anything else → reject. |
| For `json_schema`: `json_schema.name` is a non-empty string ≤ 64 chars, regex `^[A-Za-z0-9_.-]+$` | Hard | vLLM uses the name as a grammar identifier. |
| For `json_schema`: `json_schema.schema` is an object | Hard | Reject arrays/strings/null. |
| Max schema depth | **≤ 5** | Counts nesting through `properties`, `items`, `additionalProperties`, `anyOf`/`oneOf`/`allOf` element schemas, `prefixItems`. The 200-level attack above fails at depth 6. |
| Max serialized schema size | **≤ 16 KiB** | Measured on the marshalled `json_schema.schema` bytes. Bounds memory/time on the vLLM grammar compiler. |
| Max schema node count | **≤ 128** | Each visited schema object (root, every nested property/item/branch arm) counts as one node. Bounds breadth attacks like a single `properties` map with hundreds of children. |
| `$ref`, `$defs`, `definitions` | Forbidden | Prevents cycles and indirect blow-ups. |
| `anyOf`/`oneOf`/`allOf` array length | ≤ 16 | Avoid combinatorial explosion in vLLM's grammar compiler. |
| `enum` length | ≤ 256 | Same reason. |

Order of checks: type → name + regex → walk the schema once, counting depth, nodes, branch arms, enums, refusing on the first violation → only then `json.Marshal` the schema to enforce the byte-size cap. Walking comes first because `json.Marshal` is O(input size); doing it before the walk lets an attacker amplify a 200-level recursive payload into hundreds of allocations before the depth check ever fires. Walking first cuts that path from ~87 µs / 1606 allocs to ~560 ns / 2 allocs (Apple M2 Pro). Single bounded pass — no recursion without a depth counter.

Implementation: `paramvalidators.ResponseFormatValidator` in `cmd/devshardctl/paramvalidators/response_format.go`, wired into the main catalog via `DocumentValidatorHandler` at `RequestFilterStagePreValidation`. The walker policy is *inverted*: it descends into every object-valued or array-of-object-valued field except an explicit list of data carriers (`enum`/`const`/`default`/`examples`/`required`/`dependentRequired`), so new JSON-Schema keywords (`if`/`then`/`contains`/`unevaluatedProperties`/...) cannot smuggle deep nesting or `$ref` past the validator.

### Shared schema-walking infrastructure

Schema-walk rejection categories are exposed as sentinel errors: `ErrSchemaDepth`, `ErrSchemaNodes`, `ErrSchemaSize`, `ErrSchemaRef`, `ErrSchemaEnum`, `ErrSchemaBranch`, `ErrSchemaType`, `ErrSchemaPattern`. They are shared by `SchemaBounds` (JSON-Schema-aware, used for `response_format` and `tools[].function.parameters`) and `ObjectBounds` (plain-object, used for `chat_template_kwargs`).

Calibrate the thresholds (depth, size, nodes) by running the validators against the sample structured-output schemas from production clients before raising any limit.

## Performance characteristics

End-to-end `normalizeChatRequest` (Apple M2 Pro, `-benchtime=2s`, `-count=3`):

| Body | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| Minimal (47 B) | 3,140 | 2,428 | 41 |
| Typical (132 B) | 4,963 | 2,926 | 65 |
| Heavy (~620 B) | 21,054 | 12,951 | 240 |
| WithResponseFormat (279 B) | 10,372 | 6,663 | 137 |
| RejectedUnknown (72 B) | 1,948 | 2,148 | 34 |
| **RejectedDeepBody (7.5 KiB body-depth attack)** | **1,093** | **96** | **2** |
| RejectedRecursiveSchema (walker reject) | 8,147 | 7,928 | 100 |

`Heavy` carries `chat_template_kwargs` so `ChatTemplateKwargsValidator` runs. `RejectedDeepBody` trips the body-level pre-scan (depth > 32) before any validator. `RejectedRecursiveSchema` reaches the walker (body depth under cap, schema depth over `ResponseFormatMaxDepth=16`) and pays for `json.Unmarshal` — ~8× more expensive than pre-scan reject.

`response_format` validator in isolation (`paramvalidators/response_format_bench_test.go`):

| Path | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| Absent | 8.7 | 0 | 0 |
| `type=text` / `json_object` | ~18 | 0 | 0 |
| Simple schema | 1,763 | 536 | 19 |
| At limits (~121 nodes) | 56,734 | 21,252 | 724 |
| Rejects recursion attack | 1,215 | 176 | 4 |
| Rejects oversized schema | 17,402 | 514 | 17 |

`CheckSize` switched from `json.Marshal(schema)` to `json.NewEncoder` over a counting `io.Writer`: same byte total, no marshaled buffer allocation. Saves -14% B/op on `At limits` and -97% on `Rejects oversized` (the 17 KiB schema no longer materializes).

Bench files: `request_filters_bench_test.go` (pipeline-level) and `paramvalidators/response_format_bench_test.go` (validator-level).

## References

Re-check these periodically for status changes (fixed-in versions, new variants, advisory updates). Sorted by relevance to this gateway.

### Security advisories covered by current filters

| ID | Title | Coverage in this gateway |
| --- | --- | --- |
| [CVE-2025-48944](https://nvd.nist.gov/vuln/detail/CVE-2025-48944) ([vLLM advisories index](https://github.com/vllm-project/vllm/security/advisories)) | xgrammar crash on invalid `type` or pattern | `SchemaBounds.validateSchemaTypeField` + `validateSchemaPatternField`. Walked on every `response_format` and `tools[].function.parameters` node. |
| [CVE-2025-61620](https://nvd.nist.gov/vuln/detail/CVE-2025-61620) | `chat_template_kwargs` Jinja injection | `ChatTemplateKwargsValidator.forbiddenChatTemplateKwargsKeys` denylist (`chat_template`, …). |
| [CVE-2025-62426](https://nvd.nist.gov/vuln/detail/CVE-2025-62426) | `tokenize=True` stalls the request handler | Same key denylist (`tokenize` rejected). |
| [CVE-2026-34756 / GHSA-3mwp-wvh9-7528](https://github.com/vllm-project/vllm/security/advisories/GHSA-3mwp-wvh9-7528) | Unbounded `n` causes OOM | `CapUintParameterHandler` clamps `n ≤ 5`. |

### Security advisories handled outside the gateway (ops-side)

| ID | Title | Handling |
| --- | --- | --- |
| [CVE-2025-9141 / GHSA-79j6-g2m3-jgfw](https://github.com/vllm-project/vllm/security/advisories/GHSA-79j6-g2m3-jgfw) | RCE via `eval()` in `qwen3_coder` tool-call parser | Per-model ops constraint: keep `hermes` parser on Qwen3-235B-Instruct. |
| [CVE-2025-48887 / GHSA-w6q7-j642-7c25](https://github.com/vllm-project/vllm/security/advisories/GHSA-w6q7-j642-7c25) | ReDoS in `pythonic` tool-call parser | Engine must not run with `--tool-call-parser pythonic`. |
| [CVE-2026-44223 / GHSA-83vm-p52w-f9pw](https://github.com/vllm-project/vllm/security/advisories/GHSA-83vm-p52w-f9pw) | Penalty fields crash EngineCore with `extract_hidden_states` spec decode | Pin vLLM ≥ 0.20.0 or keep that spec-decode method disabled. |
| [CVE-2026-44222 / GHSA-hpv8-x276-m59f](https://github.com/vllm-project/vllm/security/advisories/GHSA-hpv8-x276-m59f) | Special-token literals (`<\|vision_start\|>`, etc.) crash VL models | **Open** — Kimi-K2.6 is multimodal; vLLM with the multimodal pathway enabled is exposed via text content. A content sanitizer is needed (tracked in `❌ Open / not yet implemented` above). |

### Qwen3 upstream issues to monitor

Output-side parser bugs the gateway cannot prevent — track upstream fix status to know when 500-rate from these is expected to drop:

- [vllm-project/vllm#17790](https://github.com/vllm-project/vllm/issues/17790) — hermes parser `JSONDecodeError` on multiple tool-call blobs
- [vllm-project/vllm#27447](https://github.com/vllm-project/vllm/issues/27447) — `enable_thinking=false` breaks guided decoding
- [vllm-project/vllm#29814](https://github.com/vllm-project/vllm/issues/29814) — Qwen3 reasoning parser edge cases
- [vllm-project/vllm#39677](https://github.com/vllm-project/vllm/issues/39677) — structured output + thinking interaction
- [vllm-project/vllm#40875](https://github.com/vllm-project/vllm/issues/40875) — related guided-decoding/thinking bug
- [vllm-project/vllm#42021](https://github.com/vllm-project/vllm/issues/42021) — `<tool_call>` inside `<think>` breaks hermes parsing

### Model documentation

- [Moonshot AI — Kimi Chat API](https://platform.kimi.ai/docs/api/chat)
- [Moonshot AI — Kimi K2.6 quickstart](https://platform.kimi.ai/docs/guide/kimi-k2-6-quickstart)
- [Qwen3-235B-A22B-Instruct-2507 model card](https://huggingface.co/Qwen/Qwen3-235B-A22B-Instruct-2507-FP8)

### Upstream projects

- [vLLM security advisories](https://github.com/vllm-project/vllm/security/advisories) — review on engine upgrades.
- [vLLM releases](https://github.com/vllm-project/vllm/releases) — verify the running image's `__version__` here against the advisory "fixed in" notes above.
