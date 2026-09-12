# GLM-5.3-Flash (`zai-org/GLM-5.3-Flash`) — overrides & extensions

Provider: Z.ai. This doc documents how GLM-5.3-Flash deviates from the [universal contract](README.md). For params that behave the same as universal, see the universal contract directly.

Mirrors the structure of [DeepSeek-V4-Flash-0731](deepseek-v4-flash-0731.md) and [MiniMax-M2.7](minimax-m2.7.md). Thinking cannot be switched off on this model, so the whole thinking contract is documented here. Chain-side wiring (HuggingFace revision pin, ModelArgs) comes from the chain model registry (`models_all` query).

## Model facts

| Property | Value | Source |
|----------|-------|--------|
| Provider | Z.ai | [[Zai-3]](references.md#zai) |
| vLLM route id | `zai-org/GLM-5.3-Flash` | — |
| Total params | 320 B mixture-of-experts, 18 B active | [[Zai-3]](references.md#zai) |
| Context window | 1 M tokens per the vLLM recipe; the chain registers `--max-model-len 400000` | [[vLLM-43]](references.md#vllm), chain model registry |
| Native thinking | always on — the generation prompt opens `<think>` on every turn and the template has no switch | [[Zai-1]](references.md#zai) |
| Reasoning effort levels | three: `low`, `high`, `max`; `max` when omitted or set to anything else | [[Zai-3]](references.md#zai), [[Zai-1]](references.md#zai) |
| Tool-call parser (vLLM) | `glm47`, with `--enable-auto-tool-choice` | [[vLLM-43]](references.md#vllm), chain model registry |
| Reasoning parser (vLLM) | `glm45`, which resolves to the `glm47_moe` parser | [[vLLM-43]](references.md#vllm), [[vLLM-41]](references.md#vllm) |
| HuggingFace revision pinned on chain | `04c4e9e95c5da8862dced7e5056455116f83a7e0` | chain model registry |

## Deployment requirements

Infrastructure-level constraints that must hold BEFORE this route is served — enforced by vLLM engine configuration, NOT by the gateway:

- **vLLM ≥ 0.29.0 per the vLLM recipe**, which also marks a nightly build as required and ships the model in a dedicated `vllm/vllm-openai:glm53-flash` image until support lands in the standard one ([[vLLM-43]](references.md#vllm)). The gonka-ai vLLM fork has a `release/v0.28.0-glm53` branch; which build the hosts run is not recorded in this repository.
- **`--trust-remote-code` is in the registered ModelArgs**, which puts the deployment inside the blast radius of [[CVE-12]](references.md#security-advisories). Mitigation is the same as every other route: the chain pins the HuggingFace revision above, never `main`.
- **Parsers as registered: `--tool-call-parser glm47` and `--reasoning-parser glm45`**, matching the recipe ([[vLLM-43]](references.md#vllm)). That reasoning parser switches extraction off on a false `thinking`/`enable_thinking` kwarg this template never reads, which the gateway works around — see [Known model-side bugs we work around](#known-model-side-bugs-we-work-around).

## The thinking contract

The template has no thinking switch. The generation prompt always ends in `<|assistant|><think>`, and the only thinking-related variables it reads are `reasoning_effort` and `clear_thinking` ([[Zai-1]](references.md#zai)). `enable_thinking` and `thinking` appear nowhere in it.

vLLM forwards the top-level `reasoning_effort` into the template itself ([[vLLM-35]](references.md#vllm)). The template keeps `low` and `high`, renders every other value as `max`, and injects a single `Reasoning Effort: <Level>` system line at the start of the prompt ([[Zai-1]](references.md#zai)):

| Wire value | Rendered effort | Thinking |
|---|---|---|
| `low` | `Low` | on |
| `high` | `High` | on |
| `max` | `Max` | on |
| `none` | `Max` | **on** |
| `minimal`, `medium`, `xhigh` | `Max` | on |
| *absent* | `Max` | on |

**`reasoning_effort: "none"` does not turn thinking off on this route — it asks for the largest budget.** The same holds for `reasoning: {"enabled": false}`, which the gateway records as `none` ([why](troubleshooting.md#translate-reasoning)), and for the OpenAI-style `minimal` and `medium`: a client porting a request that asks for little reasoning lands on the most. `low` and `high` are the only values below the default.

A caller's `enable_thinking: false` or `thinking: false` never reached the template, but it did switch vLLM's reasoning parser off and leave the scratchpad and a dangling `</think>` in `content` ([[vLLM-40]](references.md#vllm)). The gateway therefore reports thinking as on for every request on this route — see [Gateway wiring](#gateway-wiring).

How much prior reasoning is replayed in history is controlled by `clear_thinking` — see [Native extensions](#native-extensions).

## Parameter overrides

*Delta from [universal contract](README.md#supported-parameters-universal-behavior).*

| Param | Universal | On GLM-5.3-Flash | Why |
|-------|-----------|------------------|-----|
| `chat_template_kwargs.enable_thinking` / `chat_template_kwargs.thinking` | pass-through after the bounds and forbidden-key filter | **overruled**: `enable_thinking` set to `true` and `thinking` removed, whatever the caller sent | [why](troubleshooting.md#force-enable_thinking-glm53) |
| `enable_thinking` (top-level) | translated to `chat_template_kwargs.enable_thinking` | translated, then overruled to `true` as above | [why](troubleshooting.md#force-enable_thinking-glm53) |
| `reasoning_effort` | enum-validated and forwarded on every route | forwarded; rendered as `low`, `high` or `max` per the table above, and never turns thinking off | [[Zai-1]](references.md#zai), [[Zai-3]](references.md#zai) |
| `thinking` (top-level object) | normalized to `enabled`/`disabled` and forwarded; mirrored to `chat_template_kwargs.thinking` on Kimi | normalized and forwarded with no effect: vLLM declares no top-level `thinking` field, and the template reads no such variable | [[vLLM-1]](references.md#vllm), [[Zai-1]](references.md#zai) |

## Native extensions

*Params unique to this route — no equivalent in the universal contract.*

| Param | Type | Behavior | Source |
|-------|------|----------|--------|
| `chat_template_kwargs.clear_thinking` | bool, default `false` | Passes through the `chat_template_kwargs` filter unchanged. With `false`, every prior assistant turn is replayed with its reasoning inside `<think>…</think>`; with `true`, only assistant turns after the last user message keep it, and earlier ones render an empty `<think></think>`. The model card recommends `true` for chat scenarios. | [[Zai-1]](references.md#zai), [[Zai-3]](references.md#zai) |
| `messages[].reasoning` / `messages[].reasoning_content` on assistant turns | string | Pass-through. vLLM renames `reasoning_content` to `reasoning` on input and hands the template both names, and the template replays the value according to `clear_thinking`. Without the field, the template falls back to splitting a `<think>…</think>` block out of `content`. | [[vLLM-1]](references.md#vllm), [[vLLM-42]](references.md#vllm), [[Zai-1]](references.md#zai) |

## Response shape

vLLM's canonical response field is `reasoning`; `reasoning_content` is the deprecated alias ([[vLLM-1]](references.md#vllm), [[vLLM-36]](references.md#vllm)). With the gateway's thinking override in place, the `glm45` parser moves the scratchpad into that field and leaves `content` clean, in streaming and non-streaming responses alike. The model emits tool calls as `<tool_call>name<arg_key>…</arg_key><arg_value>…</arg_value></tool_call>`, which the `glm47` parser converts to OpenAI `tool_calls[]` ([[vLLM-41]](references.md#vllm)).

## Known model-side bugs we work around

- **Reasoning leaks into `content` on a false thinking kwarg** ([[vLLM-40]](references.md#vllm)): vLLM's `glm47_moe` parser gates extraction on `thinking`/`enable_thinking`, which this template never reads, so a request carrying a false value — sent by the client, lifted by the gateway, or derived by vLLM from `reasoning_effort: "none"` — gets the scratchpad and a dangling `</think>` as its answer. Worked around request-side by the gateway ([why](troubleshooting.md#force-enable_thinking-glm53)); the upstream issue is still open.

## Known issues (not worked around)

- **`developer` messages never reach the model.** The template renders only `system`, `user`, `assistant` and `tool` messages ([[Zai-1]](references.md#zai)), and vLLM passes a `developer` message to it with the role unchanged ([[vLLM-42]](references.md#vllm)), so its content is dropped from the prompt without an error. The gateway accepts the role on every route and does not rewrite it. Send instructions as `system` on this route.

## Gateway wiring

The override is one rule on the `chat_template_kwargs` catalog entry, scoped to the exact route id and run at the `PostLimits` stage, after every lift: by then a top-level `enable_thinking` has been moved into `chat_template_kwargs`, and `reasoning: {"enabled": false}` has become `reasoning_effort: "none"`. Setting `enable_thinking` explicitly also stops vLLM from deriving `false` from `reasoning_effort: "none"`, because vLLM derives the kwarg only when the caller did not set it ([[vLLM-35]](references.md#vllm)). The rendered prompt does not change, since the template never reads the variable.

The scope is the exact id on purpose: the GLM-5.2-FP8 template does read `enable_thinking` and renders an empty `<think></think>` when it is false ([[Zai-2]](references.md#zai)), so forcing it there would take away a switch that works.

## See also
- [Troubleshooting](troubleshooting.md)
- [References](references.md)
- [Universal contract](README.md)
- [DeepSeek-V4-Flash-0731 overrides](deepseek-v4-flash-0731.md)
- [MiniMax-M2.7 overrides](minimax-m2.7.md)
