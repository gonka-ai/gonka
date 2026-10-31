# `filters` — the request and response boundary

Everything a client sends is normalised here before it reaches a host, and everything a host returns is folded and stripped here before it reaches a client. This is the only package in the gateway with no dependency on any other: it is pure, and its behaviour is pinned byte-for-byte against the goldens in `testdata/`.

## What it owns

**The request side.**
- `table.go` — one rule table, one row per top-level chat-completion parameter, each naming the stage it runs in and what it does. A parameter absent from the table is rejected: the gateway does not forward what it has not been taught.
- `presets/*.json` — the built-in presets and the models they bind; `presets.go` loads them and composes settings; `filter_knobs.go`, `template_kwargs.go` — the knobs and their bounds; `profiles.go` — the `Profile` every rule reads; `profile_set.go` — the operator's bindings the request path reads; `rules_sampling.go` — the per-model sampling limits.
- `rules_*.go` — the rule bodies: types, ranges, arrays, schemas, token budgets, reasoning controls.
- `messages.go`, `messages_validate.go` — message hygiene, which is separate from parameter rules because a message is a nested document: one file rewrites the array, the other decides whether what is left is admissible.

**The response side.**
- `fold.go` — `BodyFolder` folds the host's SSE stream into one JSON body **as chunks arrive**, stripping the fields the client must not see before merging rather than after. A client that did not ask for logprobs never accumulates them. `fold_template.go` appends the text of a chunk that differs from the last merged one only in its content, without decoding it.
- `stream.go`, `sse.go`, `stream_chunks.go` — the rewriter for a streaming client, which does the same strip per event on the way out, over the SSE framing primitives and the fold that serves a whole completion as chunks.
- `response.go` — which fields are stripped, and which a client can ask back.
- `cacheable.go` — the one walk that decides whether a reply may be stored: what it failed with, and whether its answer finished.
- `finish.go` — whether every choice a reply started also finished, which is what that walk asks besides the error.
- `vocabulary.go` — the name each refusal goes into the record under.
- `assemble.go` — the whole-body fold, the reference implementation the incremental one is verified against.

## Boundaries

- **The gateway forces `stream: true` upstream** even when the client did not ask for it, so both sides of this package are always in play.
- **Three fields are never returned to anyone**: `token_ids`, `prompt_token_ids`, `prompt_logprobs`. Three more are returned only if asked: `logprob`, `logprobs`, `top_logprobs`.
- **The strip list is derived, not written twice.** A field added to what is stripped cannot be left out of what is always stripped — the failure that exposes `top_logprobs`.
- **A malformed body passes through unchanged rather than being dropped**, except where a host writes `NaN`/`Infinity` as barewords, which are normalised so the body is inspectable at all.

## The request pipeline

`NormalizeRequest` parses the body into a `Document`, then `runPipeline` walks it in a fixed order:

1. Resolve the routed model — the body's `model` when it is a non-blank string, otherwise `Options.RoutedModel` — and pick that model's `*Profile`.
2. Unwrap `extra_body`.
3. Reject any parameter absent from the table.
4. Run every `StagePreValidation` rule.
5. Normalise the messages, then validate them.
6. Decode the typed `requestView`: `model`, `stream`, `max_tokens`, `max_completion_tokens`, `n`.
7. Resolve the output-token limits and write them back into the document.
8. Read the client's `LogprobIntent`.
9. Run every `StagePostLimits` rule.
10. Re-decode the view, keeping the token fields step 7 resolved.
11. Read `stream_options.include_usage` as the client's usage intent.
12. Force streaming upstream, unless `Options.KeepClientStream` is set.
13. Marshal.

Step 8 has to sit where it does: `StagePostLimits` caps `top_logprobs`, so after it the document says the width that goes on the wire, not the one the client asked for.

### Registration order is semantics

Rules run in `parameterTable` order, so where a row sits is behaviour rather than style:

- `logit_bias`'s key check runs before its value and size rules.
- `min_tokens` runs before `tools`, so a malformed value is rejected before `validTools` drops it for a `"required"` choice.
- `tools` runs before `tool_choice`, because `validTools` coerces a `"required"` choice and can delete both fields outright.
- `parallel_tool_calls` runs after `tools`, because its own rule drops the field whenever `tools` did not survive that row — a plain `Has("tools")` check on whatever `validTools` left behind.
- `thinking` runs before `chat_template_kwargs`, so a mirrored value is already in place when the kwargs bounds are checked.

### `extra_body` and the whitelist

`unwrapExtraBody` flattens an `extra_body` envelope into top-level fields before the whitelist runs. An existing top-level key wins on conflict, a nested `extra_body` key is ignored, and the envelope itself is dropped either way.

`rejectUnknownParameters` then rejects any key absent from `parameterTable`. The unknown keys are sorted and only the first is reported, so the rejection is deterministic; a key with an empty name gets its own message. The rejection text names the reason — a non-standard parameter can crash the vLLM engine on a host — and points at the network's chat-API docs and issue tracker.

### Streaming is forced upstream

`forceUpstreamStreaming` sets `stream: true` and `stream_options: {"include_usage": true}` on every host request unless `KeepClientStream` says otherwise. The forced `stream_options` map is shared rather than rebuilt per request: the document is marshalled and dropped straight after, so nothing can mutate it.

`Result.ClientStream` and `Result.ClientUsage` carry what the client actually asked for, which is what the response side needs in order to undo the forcing.

## Structural bounds before the decode

Three bounds run before anything is decoded: `MaxBodyBytes` (10 MiB) on the raw body, `MaxNestingDepth` (32), and `MaxStructuralNodes` (250,000) counting containers and elements.

`ensureStructuralBounds` scans the body byte by byte in a single pass, tracking string literals and escapes so that braces and commas inside a string are not counted. A closing bracket never drives the depth below zero, so an unbalanced body is bounded rather than rejected here — the decoder that follows is what rejects it.

## Which JSON decoder, and why

The package uses two: `encoding/json` from the standard library, and `github.com/goccy/go-json`. Each site picks one for a stated reason.

| Site | Decoder | Why |
| --- | --- | --- |
| `ParseDocument` | standard library | Its error text goes to the client verbatim, and the golden corpus pins that wording. A faster library with different phrasing would change what every malformed request sees. |
| `stripInternalFields`, `decodeStreamedEvent`, `completionAsChunks` | standard library | goccy parses a number token even into a `json.RawMessage` and errors past the float64 range. That fails the strip open — a body carrying `1e999` would keep every internal field — and fails the chunk conversion on a `created` out of range. |
| `Document.Marshal`, `DecodeUpstreamError`, `jsonMarshaledSize` | goccy | Neither the error text nor the range behaviour is load-bearing here, and `Marshal` sorts map keys, so the normalised body is deterministic. |

Every goccy decode of bytes a host or a client sent goes through `internal/strictjson`, which hands goccy only what `encoding/json.Valid` accepts; what an operator configures, such as the admin overrides and the rotation models, is decoded directly. `transport` cannot import it, so its two decodes of a chat reply, the SSE envelope (`HTTPClient.handleSSELine`) and the JSON body (`HTTPClient.Send`), run `encoding/json.Valid` themselves; both are reached only from a chat send, which host binaries never make: they reach `HTTPClient` through peer and executor clients with no `Send`. On some malformed input goccy reads past its buffer and panics, intermittently and more often under concurrency; the attempt classifier decodes every event of every attempt, so one malformed line from a host would otherwise be able to take the gateway down.

Every decode that matters uses `UseNumber`, so a number survives as its literal rather than as a float64.

Encoding goes through `encodeCompact`, which uses an `Encoder` rather than `Marshal` for one reason: only the encoder can call `SetEscapeHTML(false)`, and the default would inflate every `<`, `>` and `&` the model generated to six bytes. The newline `Encode` appends is trimmed off, since it is not part of the value. `growingText.MarshalJSON` goes through it for the same reason. `jsonMarshaledSize` measures with a counting writer instead of allocating the output, subtracting the one byte that newline adds.

## Parameter rules

Type rules differ on what an explicit `null` means. `requireUint` and `requireBool` treat a null like an absent field and pass it through; `requireString` and `validModelName` reject it, because a null is not a string.

Numeric rules run through `sanitizeFloatField`, which coerces the field to float64 and *deletes* it when it is absent, unparseable, or non-finite — so `NaN` and `Inf` never reach a host.

- `clampFloat` clamps into `[min, max]` and writes the result back. It backs `temperature` (0–2), `min_p` (0–1), and both penalties (-2–2).
- `rejectNonPositiveThenClamp` clamps down to the maximum and *then* rejects a non-positive result. The order is the point: an exclusive lower bound cannot be enforced by clamping without producing an illegal value. It backs `top_p` and `repetition_penalty`.
- `validTopK` accepts only `-1` (disabled) or a value at or above 1, then clamps down to 262144 and truncates toward zero.

Collection rules:

- `validListLength` bounds an array's length and, optionally, the length of its string elements. `messages` is capped at 2048 entries, `stop` at 16 entries of 256 bytes, `bad_words` at 64 entries of 128 bytes.
- `requireStringElements` rejects the first non-string element, reported as `<param>[<index>]`.
- `dropBlankStringListElements` removes whitespace-only strings from `bad_words` and drops the field when nothing survives.
- `validFloatMap` rejects `logit_bias` outright past 1024 raw entries, then drops individual entries outside `[-100, 100]` or non-finite, and drops the field once none survive.
- `requireTokenIDKeys` rejects a `logit_bias` key that is not a non-negative 32-bit integer (vLLM #16529). The lexicographically first offender is reported, so the message is stable across map iteration order.
- `validMetadata` enforces the OpenAI-compatible contract: an object, at most 16 keys, keys up to 64 bytes, string values up to 512 bytes.
- `validStreamOptions` strips the field entirely unless `stream` is exactly `true`, then keeps only `include_usage` and drops the field when nothing survives.

What the gateway forces, and what it strips:

- `return_token_ids` is stripped: the executing host sets it itself (`common/completionapi.ModifyRequestBodyWithLogprobsMode`) and drops the token ids from its answer, and the response-side strip still removes any that arrive.
- `logprobs` and `top_logprobs` are **carried, not forced**: a non-boolean or negative ask is rejected, and a width above `completionapi.ForcedTopLogprobs` is capped to it. The host re-pins that same constant on every executed request, so overwriting the client here would buy nothing and lose what it asked for. A narrower ask is accepted but not honoured — the host executes at the pinned width and the answer carries it.
- `n` is replaced with `1` when present: the reservation budgets one `max_tokens` worth of output, and `n` choices can produce `n` times what it signed for.
- Silently stripped: `service_tier`, `store`, `provider`, `plugins`, `prompt_cache_key`, `cache_key`, `extra_headers`, `thinking_config`, `think`. `stop_token_ids` are checked, not stripped; see "Stop token ids".
- `model` must match `^[A-Za-z0-9._/-]+$` and stay under 256 bytes; `user` and `safety_identifier` under 512.

## Output token limits

`DefaultRequestMaxTokens` (4096) and `RequestMaxTokensCap` (4096) are the package defaults, and also the single source `config.Defaults` reads. `Options` may override either globally, and `Options.ModelTokenLimits` may override either per routed model — a zero returned there means "not set for this model" and leaves the global one alone.

`capOutputTokens` treats zero as "the client named no budget": it returns the configured default, which the cap does not clamp. A nonzero value is clamped to the cap unless `Options.Admin` bypasses it. `MaxOutputTokens` (10 000 000) is above all of them: every path out of `capOutputTokens` clamps to it, the admin bypass included, and `config.Validate` refuses a `max_tokens_cap`, a `default_max_tokens` or a per-model override past it. A budget that cannot exceed the ceiling is a budget the host's output window can be priced from without the arithmetic leaving `int64` (see [`docs/capacity.md`](../docs/capacity.md), "The participant limiter: IOCW").

`applyOutputTokenLimits` then resolves one number from whichever fields the client sent — the minimum of both when both are present — and writes it to `max_tokens`. It mirrors the result into `max_completion_tokens` only when the client sent that field, so the gateway does not introduce a field the request never carried. No shared floor raises it: a short answer is a request the protocol prices and validates as it is, down to the chain's one-token reservation (`state.MinReservationTokens`; see `devshard/docs/proposals/short-output-validation.md`).

`keepCallerMinTokens` forwards only a `min_tokens` the client sent, clamped to the resolved `max_tokens`. A zero, or a request decoding under a structured-output grammar (`completionapi.GrammarConstrained`: a `json_object`, `json_schema` or `structural_tag` response format, a `structured_outputs` constraint, a required or named tool choice), forwards none, because once the grammar allows only EOS, masking EOS leaves every logit at -inf. This is the rewrite `completionapi.EnforceTokenBudget` applies on the executor and on the validator's replay, so the three agree on the request they price and judge. The grammar check reads the body as it leaves the pipeline, where a `"required"` choice `validTools` relaxed to `auto` no longer shows. The caller still forced a call, and with no grammar to close it, masking EOS pads the call's arguments into invalid JSON, so `validTools` drops `min_tokens` itself when it relaxes the choice. A caller's own `auto` forces no call and keeps its `min_tokens`.

`raiseOutputTokensToProfileFloor` runs first and raises a present, numeric `max_tokens` or `max_completion_tokens` under the profile's own `OutputTokensFloor` to it. Only Kimi declares one, 16, because below it Kimi-K2.6 emits only its closing think tag. It has to run before the refusal, because `capOutputTokens` reads a zero as "the client named no budget" and would hand back the default instead. `rejectNonPositiveOutputTokens` then rejects whatever is left, on every profile: a zero output budget makes no answer, and the redundancy layer then waits out a winner that cannot come.

## The prompt text

`PromptText` reads a normalized body back into the text a host's chat template renders: each message's content, each tool call's name and arguments, and the `tools` array as its JSON, in order, without the template's own tokens. A content that was text parts is one string by then (`flattenMessageTextParts`). A field that is not a string is written as compact JSON without the HTML escapes (`\u003c`, `\u0026`) the body's own encoding adds, since the templates render `<` and `&` as themselves, and with its numbers as the client wrote them. Reasoning fields are left out: templates differ on whether they render a past turn's reasoning, and the check that reads this text ([`docs/request.md`](../docs/request.md), "Step 6 in detail") must not count what a host may drop, since an over-count refuses a request a host would serve while an under-count only leaves it unchecked. It decodes the body a second time, and only for a model with a loaded tokenizer; a string value is decoded straight into a string, and only a value that is not one goes through the generic decode and re-encoding.

## Stop token ids

`stop_token_ids` reach the engine only when every id is an integer inside the routed model's vocabulary (`validateStopTokenIDs`, through `completionapi.ValidateStopTokenIDs`): an out-of-vocabulary id asserts on the node. The document decodes numbers as `json.Number`, so an id written with a fraction, even an integral one like `1.0`, is refused here although the host, decoding it as a float, would accept it. The size comes from `Options.StopTokenVocabulary`, which the package calls only when the request carries a non-empty list, since the lookup can block on the chain. An unknown size — no function wired, or one that answers 0 — refuses the field with a 400, because no id can be proven safe without it. The check runs after the pre-validation stage, so a request another field's rule refuses fails there first.

## Model profiles

A `*Profile` is one routed model's set of deltas from the default pipeline. A nil profile *is* the default — Qwen, DeepSeek and anything unrecognised.

A request's profile is resolved once, by `Options.Profiles.For(model)`: an operator setting stored through `/v1/admin/filters` wins, otherwise the built-in binding (`BuiltInProfileFor`), otherwise the `default` preset. `For` never returns nil, and every rule reads the profile through a nil-safe accessor that resolves nil to the same `default` preset. `Result.EmptyAnswerCountsTokens` carries the profile's empty-answer exemption on to the engine, so the race is classified by the same profile the body was normalised with.

### Presets live in `presets/`

Every built-in preset is a JSON file in [`presets/`](./presets), embedded into the binary. `default.json` spells out every choice a profile must make (thinking, its template key, the reasoning_effort mode, tools, parallel_tool_calls); every other preset is composed over it, so an unset choice can only come from a broken file, and the loader refuses it. A file is `{"models": [...], "filters": {...}}`. The `filters` object is the same one a `PUT /v1/admin/filters/{model}` carries, so a built-in preset and an operator setting are written the same way; `models` binds models to the preset when no operator setting names them. The preset's name is the file name. `loadPresets` decodes every file through `strictjson.DecodeKnownFields`, one value and no unknown keys, and composes it like an operator setting, and refuses the whole folder on any bad file or on a model two files bind, so a typo fails `TestEveryEmbeddedPresetComposes` rather than booting as half a profile. Adding a preset is adding a file; no Go code names a model.

| Preset | Built-in binding | Deltas |
| --- | --- | --- |
| `kimi` | `moonshotai/Kimi-K2.6` | Zero penalties forced, `structured_outputs` rejected, `safety_identifier` allowed, thinking mirrored into `chat_template_kwargs.thinking`, a `thinking_token_budget` resolution, the empty-answer exemption, an output budget under 16 raised to its own floor instead of refused. |
| `minimax` | `MiniMaxAI/MiniMax-M2.7` | Thinking fields stripped, `reasoning_split` kept. |
| `glm_flash` | `zai-org/GLM-5.3-Flash` | Thinking forced on in `chat_template_kwargs.enable_thinking`. |
| `default` | everything else | None. |

### Knobs

`ComposeProfile(model, ModelSetting)` turns a setting into a fresh profile: the named preset, or the model's own built-in preset when none is named, or `default`; then each `FilterKnobs` field that is set; then `validate`. A knobs-only setting is therefore a tweak of the model's preset, never a reset: Kimi keeps its budget and exemption when an operator only raises its floor. A partial `reasoning_effort` object keeps the preset's mode or allowed list for the half it leaves out. Every knob only removes, zeroes, refuses, narrows or raises what the client sent, and none can loosen a guard in the rule table.

| Knob | Shape | Default | Rule it changes |
| --- | --- | --- | --- |
| `thinking` | `normalize`, `mirror_to_kwargs`, `strip`, `force_on` | `normalize` | `thinking`, `enable_thinking`, `chat_template_kwargs` |
| `thinking_template_key` | kwargs key, ≤ 64 bytes, never a forbidden key | `enable_thinking` | the key the mirror, force-on and budget silence write |
| `thinking_token_budget` | `{force_zero_below, default_divisor, absolute_max, content_headroom}` or absent | absent: the field is stripped | `thinking_token_budget` |
| `empty_answer_counts_tokens` | bool | false | the engine's crowning of an empty stop with completion tokens |
| `force_zero_penalties` | bool | false | `frequency_penalty`, `presence_penalty` |
| `reject_structured_outputs` | bool | false | `structured_outputs` |
| `allow_safety_identifier` | bool | false | `safety_identifier` |
| `keep_reasoning_split`, `default_reasoning_split` | bool; the default needs keep | false, none | `reasoning_split` |
| `reasoning_effort` | `{mode: forward or strip, allowed: [...]}` | forward, every value | `reasoning_effort`, `reasoning.effort` |
| `sampling` | `temperature_max`, `top_p_max`, `min_p_max`, `repetition_penalty_max`, `top_k_max`, each inside the global bound | none | the sampled parameters, after their global rule; `top_k: -1` becomes the model's max |
| `template_kwargs` | `{forced: {key: bool, number or string ≤ 256}, denied: [key]}`, ≤ 16 each, never a forbidden key | none | `chat_template_kwargs`, after every thinking rule: denied keys removed, forced keys written |
| `tools` | `allow`, `reject` | `allow` | `tools` is a 400 when rejected |
| `parallel_tool_calls` | `keep`, `strip` | `keep` | `parallel_tool_calls` |
| `reject_json_schema` | bool | false | `response_format` of type `json_schema` is a 400 |
| `output_tokens_floor` | 0 to `MaxOutputTokensFloor` | 0 | `max_tokens`, `max_completion_tokens` |

A budget cannot go with `thinking: force_on`, nor with its own `thinking_template_key` among the forced or denied kwargs: each would undo the silence below `force_zero_below`, and the model would then reason through a budget too small to answer in. `force_on` cannot go with its key among the forced or denied kwargs either, since the kwargs policy runs after it. The silence also applies when the gateway's own clamps (`absolute_max`, `content_headroom`) or its invented default bring the budget to 0; a client that asked for 0 itself keeps its kwargs as sent.

`thinking_template_key` is the key the `thinking` field's mirror, `force_on` and the budget silence write. A top-level `enable_thinking` is always mirrored into `chat_template_kwargs.enable_thinking`, whatever the key: that field already names its kwarg.

`Profile` carries no JSON of its own. `Profile.Knobs` writes it out as a fully set `FilterKnobs`, the same object a PUT carries, and that is what the admin API shows as a model's effective filters and as the preset catalog. A nil profile resolves to the `default` preset everywhere, so the default is defined once, in `presets/default.json`. `PresetCatalog` is the admin API's preset listing. Every composed profile and every catalog copy is a deep copy: no caller shares a pointer, slice or map with the catalog. Only `PresetDefault` is named in Go; the other presets are named by their files.

Denied kwargs keys are removed, not refused: the thinking rules write keys such as `enable_thinking` into the object first, so a refusal would turn away requests whose client never sent the key. The globally forbidden keys stay a 400.

`thinking_token_budget` and `empty_answer_counts_tokens` are the two knobs a mistake turns against the hosts: the budget field crashes vLLM `EngineCore` on models that lack it (Qwen3-Instruct-2507), and the exemption lets a host's own usage report win the race with an empty answer (`docs/race.md`, "Crown denial"). `Profile.RequiresRegisteredModel` reports them, and the admin API accepts them only for a model the chain registers, never through `allow_off_chain`.

How filters rank against `model_limits`, the environment and the host, and what a release that changes a preset does to stored settings: [`docs/operations.md`](../docs/operations.md), "Which setting wins" and "When a release changes the presets". The profile's `output_tokens_floor` also raises the default output budget a request leaves unnamed.

`ThinkingDisposition` is the closed set of ways a profile handles `thinking`/`enable_thinking`: normalise in place (the default), mirror into `chat_template_kwargs`, strip entirely, or force thinking on.

## Reasoning and thinking

`reasoning` is a wrapper the gateway does not forward: `reasoningWrapper` deletes it, and lifts `.effort` into a not-yet-present `reasoning_effort` — unless the wrapper carries `enabled: false`, which becomes `reasoning_effort: "none"` instead.

`reasoning_effort` is validated against a closed enum (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`) and forwarded on every route: no profile scopes it away, and none supplies a default.

`enable_thinking` and `thinking` both depend on the profile's disposition:

- `ThinkingStrip` deletes the field — the model has no matching chat-template knob.
- `ThinkingMirrorToKwargs` moves the boolean into `chat_template_kwargs`, preserving a value already nested there and always removing the top-level field.
- Otherwise `thinking.type` is normalised in place to `"enabled"` or `"disabled"`, and the `display` hint is dropped. `adaptive` and `auto` both resolve to enabled: they signal opt-in thinking with an SDK-chosen budget.

`ThinkingForceOn` handles both fields as the default does, then sets `chat_template_kwargs.enable_thinking` to `true` at `StagePostLimits`, once every lift into the kwargs has landed, whatever the caller sent. A caller's `chat_template_kwargs.thinking` passes through. Why, and why only on the exact GLM-5.3-Flash id: [`glm-5.3-flash.md`](../../../../docs/chat-api/glm-5.3-flash.md).

`thinking_token_budget` reaches a host only for a profile that declares a `ThinkingTokenBudget`, which among the presets only `kimi` does: `thinkingTokenBudgetScope` strips it everywhere else at `StagePreValidation`, because the engines behind the other models crash on it (vLLM `EngineCore` on Qwen3-Instruct-2507, [`qwen3-235b-a22b-instruct-2507.md`](../../../../docs/chat-api/qwen3-235b-a22b-instruct-2507.md)) and a host on the V2 model runner rejects it outright. For the profile that keeps it, `thinkingTokenBudgetResolve` runs after the token limits, invents `max_tokens / default_divisor` (2 for Kimi) when the request names none, and clamps the result so content keeps room to be written: down to `absolute_max` (96,000), and down to `max_tokens - content_headroom` (64). Without a budget the rule removes the field, so it never reaches a host even if an earlier stage let it through.

### Silencing Kimi's reasoning

Below `force_zero_below` (256 for Kimi) output tokens, a profile that owns the budget gets `thinking_token_budget: 0` and `false` under its `thinking_template_key` (`chat_template_kwargs.thinking` for Kimi). That second write overwrites rather than fills, unlike every other mirror: the budget alone is a logits processor, which speculative decoding discards, and the `thinking` rule has already mirrored the caller's answer into the kwargs. A request that cannot afford thinking cannot afford it in the template either.

### The rest of the profile-scoped rules

- `safetyIdentifier` validates and keeps the field for a profile with `AllowSafetyIdentifier`, and strips it for every other.
- `reasoningSplit` strips the field for profiles that cannot serve it and passes it through for the one that can; an absent field stays absent. Passing it through is a courtesy, not a control: `reasoning_split` is MiniMax's own hosted-API switch and vLLM has no such request field, so a host ignores it and logs it as unused. The network's MiniMax nodes separate reasoning server-side, via `--reasoning-parser minimax_m2_append_think` in `deploy/join/node-config-minimaxm27-*.json`.
- `forceZeroPenalty` overwrites `frequency_penalty` and `presence_penalty` to 0 for a `ForceZeroPenalties` profile, but only when the field is already present — it never introduces one.

## Schema bounds

Four fields carry a nested payload, and each has its own bound family, kept separate even where the values coincide: `tools`, `response_format`, `structured_outputs`, and `chat_template_kwargs`.

`SchemaBounds.Check` walks a JSON-Schema payload before measuring its serialised size, and enforces:

- depth (16), node count (128 or 256), serialised size (16 KiB, 64 KiB for `tools[].function.parameters`), branch arms per `anyOf`/`oneOf`/`allOf` (16), and `enum` size (256).
- `$ref`, `$defs` and `definitions` are forbidden outright.
- `type` must be a JSON-Schema primitive, or an array of them; anything else crashes xgrammar's grammar compiler (CVE-2025-48944).
- `pattern` must be a string, under 512 bytes, and must compile (CVE-2025-48944).

The walk distinguishes two key families. `schemaDataKeys` — `enum`, `const`, `default`, `examples`, `required`, `dependentRequired` — hold literal data, not child schemas, so the walker must not recurse into them. `schemaChildMapKeys` — `properties`, `patternProperties`, `dependentSchemas` — hold name-to-schema maps: each value is walked as its own child schema, and the wrapper map itself is not counted as an extra node.

`ObjectBounds` is the plain-object counterpart used for `chat_template_kwargs`: depth, node count and size only, with no `$ref` ban and no `type`/`pattern`/`enum`/branch semantics, since that payload feeds a Jinja renderer rather than a grammar compiler. Both walkers share `depthExceededError` and `nodesExceededError` so they render identical rejections.

### `tools` and `tool_choice`

`validTools` enforces the OpenAI tool contract — every entry an object of `type: "function"` with a non-empty `function.name` — and does the cross-field cleanup that `tool_choice` depends on: an absent or empty `tools` deletes `tool_choice` outright, whatever value it carried; otherwise `"required"` collapses to `"auto"` and drops `min_tokens` (see "Output token limits"), an absent choice defaults to `"auto"`, and `function.strict` is stripped silently. `validToolChoice` then accepts only `"auto"`, `"none"`, or a function object with a name under 64 bytes; `"required"` is coerced upstream and never reaches it.

`parallel_tool_calls` is dropped the same way, whatever value it carried, whenever `tools` is absent or empty — vLLM's `check_tool_usage` rejects a `tool_choice` other than `"none"` sent without `tools`, and `parallel_tool_calls` controls nothing without `tools` either, so a request without tools is accepted rather than rejected over a control that names no tool call. `parallelToolCalls` is its own row, gated on `ctx.Document.Has("tools")` the way `safetyIdentifier` and `reasoningSplit` gate on the profile; when `tools` survived, it defers to `requireBool()` unchanged.

`tools[].function.parameters` gets 64 KiB rather than the 16 KiB every other schema-carrying field shares: vLLM only compiles a tool's `parameters` into an xgrammar grammar for a named `tool_choice` or `"required"`, and `"required"` is what this gateway coerces to `"auto"` above — a named choice still passes through unchanged. The structural bounds (depth, nodes, branch, enum, pattern) are what actually guard the grammar compiler either way; the byte cap is a size backstop on top of them.

### `response_format`

`text` and `json_object` pass as-is. `json_schema` additionally requires a name matching `^[A-Za-z0-9_.-]+$` and under 64 bytes, plus a schema object that clears `SchemaBounds`.

### `structured_outputs`

The six constraint fields — `json`, `regex`, `choice`, `grammar`, `json_object`, `structural_tag` — are mutually exclusive per vLLM's `StructuredOutputsParams.__post_init__`: exactly one must be set, and the rejection names the ones that were. Three auxiliary fields may accompany any of them (`whitespace_pattern`, `disable_any_whitespace`, `disable_additional_properties`), two private backend fields (`_backend`, `_backend_was_auto`) are stripped silently, and any other sub-field is rejected. The field cannot be combined with `response_format`, and a profile with `RejectStructuredOutput` rejects it outright because it has no matching route.

Each constraint pairs with its own validator in a map, so a new constraint field cannot be added without one — a coverage test checks the pairing. Per-constraint caps:

- `json` must be an object, not a string-encoded schema, and clears `SchemaBounds`.
- `regex` and `whitespace_pattern` share one validator: string, under `MaxPatternLen`, and must compile.
- `choice` is a non-empty string array, at most 256 entries, each under 1024 bytes, with a total under the shared size cap.
- `grammar` is capped at 8 KiB and 200 levels of nesting. The check tracks *active* bracket depth: unmatched opens, the CVE-2026-25048 proof-of-concept shape, drive the depth up without a close ever bringing it down.
- `structural_tag` accepts only the object form (vLLM's `StructuralTagResponseFormat`) under 4 KiB; a JSON-encoded string form crashes the engine with an HTTP 500.

### `chat_template_kwargs`

Forbidden keys are rejected before the object bounds run. They are the ones that override `apply_hf_chat_template`'s positional arguments instead of becoming template variables: `chat_template` (CVE-2025-61620, arbitrary Jinja template), `tokenize` (CVE-2025-62426, stalls the request handler), plus `tools`, `documents`, `conversation`, `continue_final_message`, `padding`, `truncation`, `max_length`, `return_tensors`, `return_dict`. `add_generation_prompt` is not banned.

## Message hygiene

Normalisation runs before validation, as a fixed and order-sensitive chain:

1. `dropOrphanToolMessages` — removes a `role: "tool"` entry whose `tool_call_id` has no matching prior assistant `tool_call`, tracking pending ids exactly the way `validateMessages` does.
2. `dropEmptyAssistantTurns` — removes an assistant message with no content and no `tool_calls`/`function_call`: an informationless placeholder some clients resend.
3. `normalizeEmptyMessageContent` — fills empty `role: "tool"` content with `<empty tool result>`, because vLLM chat templates require some text in every tool turn, and nullifies empty assistant content that carries a call payload.
4. `stripLegacyToolName` — drops the `name` field from `role: "tool"` messages, a leftover from the retired `role: "function"` shape.
5. `flattenMessageTextParts` — joins a content array of `{type: "text", text}` parts into one newline-separated string. Any other shape is left alone for validation to reject.

Validation then requires a non-empty `messages` array, and keys every message on its role. A role missing from `messageRolePolicies` is rejected, which makes the policy map the closed set of accepted roles.

| Role | Disallowed fields | Also required |
| --- | --- | --- |
| `developer`, `system`, `user` | `tool_calls`, `tool_call_id`, `function_call` | non-empty content |
| `assistant` | `tool_call_id` | content, unless `tool_calls` or `function_call` is present |
| `tool` | `tool_calls`, `function_call` | a `tool_call_id` matching a previous assistant call, and content |
| `function` | `tool_calls`, `tool_call_id`, `function_call` | a `name`, and content |

An explicit `null` counts as present for the disallowed-field check. Inside `tool_calls` and `function_call`, though, a null value is treated as absent and silently removed — some SDKs serialise empty slots that way. `tool_calls` ids must be unique, and every entry must be `type: "function"` with a `name`.

Content is accepted as a non-blank string, or as a non-empty array of `{type: "text", text}` parts. `isEmptyContent` reports a blank string or a zero-length parts array; anything else, `nil` included, is not empty — a missing field is a different failure from an empty one.

## Response stripping

`LogprobIntent` is what the client's own request asked for, read before the cap can narrow it. Without it the strip cannot tell a client who asked for logprobs from one who did not, and would answer both by removing them. `logprobs: true` without alternatives keeps `top_logprobs` as a present-but-empty array, which is OpenAI's shape for that ask. The served hosts never send that shape, though: a host forwards logprobs only when both `logprobs` and a positive `top_logprobs` were asked (`completionapi.LogprobsAsked`, read by the host's response processor), so a client asking `logprobs: true` alone gets no logprobs at all, as from devshardctl, and the empty-array path serves only a host that sends logprobs it was not asked to.

- A client that asked for nothing loses `clientStrippedFields`, at any depth: the whole logprob family plus `token_ids`, `prompt_token_ids`, `prompt_logprobs`.
- A client that asked for logprobs loses only `alwaysStrippedFields`, which is *derived* by subtracting `requestableFields` from the full list. A hand-written second list can omit a field the full list gained, which exposes `top_logprobs`.
- A client that asked for logprobs but not alternatives keeps the key with an empty array: that is the shape OpenAI returns. Leaving the forced alternatives in would hand the client a request it never made, and removing the key would drop a field its schema expects to be present.

`decodeLogprobIntent` is lenient: only an explicit `true` counts as a request, so a value of the wrong shape reads as "not asked". The gateway forwards both fields as the client sent them, `top_logprobs` capped at `completionapi.ForcedTopLogprobs`; the host pins both on for the engine, since validation needs them, and forwards the logprob family back only to a client that asked.

### Non-finite barewords

A backend writes `NaN`, `Infinity` and `-Infinity` as barewords for a probability of zero or an overflow. None is valid JSON, so a body carrying one parses nowhere: the buffered path would forward it with every internal field intact, and the streaming path would drop the event and the client's answer with it.

`ReplaceNonFiniteNumbers` rewrites them to `null` outside string literals. It returns `ok = false` when the body carries none, so the ordinary path allocates nothing, and it matches the longest literal first so `-Infinity` is not read as a minus sign followed by `Infinity`.

When that rescue fires, the caller must receive the re-encoded bytes even if no field was deleted. Handed the original, everything downstream meets the barewords again: the completion-to-chunks conversion fails on them and forwards a response a streaming client renders nothing from, while the attempt is crowned on its content and the nonce is paid for.

### Cacheability

`CacheRefusal` names why a response may not be stored: an empty body, a failure that must not be replayed, an event or a body nothing can read, an answer that never finished, or a status that is neither a success nor a deterministic client-input error (an HTTP 400 with a parseable error body). A failure carried under a success status is never stored, however deterministic its message reads: a 200 whose payload is an error answers nothing. `IsCacheableResponse` is the same answer as a boolean. On read `api` asks the narrower `HasNonCacheableError` — see [`api/README.md`](../api/README.md), "The response cache" for why only the failure is re-asked — and that walk passes `judgeAnswer` false, so a hit pays for none of the choices.

One walk decides all of it: `scanResponse` reads a plain JSON body whole and an SSE body **event by event, not `data:` line by line** — a client joins the lines of one event, so an object a host split across two of them must reach the decoder whole. Each event is decoded once into `scannedEvent`, the error shape and the choices together, because two walks would parse the same megabyte twice to ask two questions about it.

**Every shape the fleet answers a failure in is read as one.** An `error` field arrives as an OpenAI object and as a bare string, and a host that sends the string may name its class in a sibling `error_type`; a flat document says `"object":"error"` and may carry only a type. So `error` is held raw and decoded by its shape rather than typed in the struct: a bare string in a typed field fails the whole decode, and a failure nothing can decode reads as a reply carrying no error at all. An empty string and a JSON null are not failures. The same widening is why `finalizeCompletion` leaves an error document labelled `"error"` instead of relabelling it a completion: the fold is what a non-streaming caller is handed, and a relabelled error is one nothing downstream can recognise.

A host that types its `choices` as something no client could render must not take the error in the same event down with it — that is how a rate-limit failure would end up replayed for an hour. So the decode falls back to the failure shape alone, and an event read that way counts as unreadable rather than as silence. An event nothing can read at all — a truncated tail, a payload that is not JSON — refuses the reply outright, since what it carried is unknowable.

Whether a host's error may be replayed is read from what the host itself said, in the order of how much that is:

1. **The status it named.** A numeric `code` is the status the host would have answered with: 400 and 422 are about the request and may be stored, while 404 names a model another host may still serve, and 408, 429 and 5xx are about the moment.
2. **The class it named.** A `type`, or a `code` that is not a status, is a class name — `server_error`, `BadRequestError`, `rate_limit_exceeded` — matched with every separator removed. A substring of a class field is still a class; a substring of a message is prose.
3. **The words of the message**, when the host gave nothing else: `momentaryFailureMessages` names cancellation, timeouts, rate limits, overload, unavailability and model availability.

The two structured tiers are not guesses about the hosts: vLLM's `create_error_response` (`vllm/entrypoints/serve/exception_handling/error_response.py`) fills `ErrorInfo.code` with the `HTTPStatus` it would have answered with, and fills `type` with the class it raised — `BadRequestError` at 400, `UnprocessableEntityError` at 422, `NotFoundError` at 404, `InternalServerError` at 500 — or, for a graceful HTTP error, with the status phrase itself (`Service Unavailable`, `Too Many Requests`), which is why a class is matched with its separators removed. Older vLLM wrote the flat `{"object":"error",...}` shape instead; `DecodeUpstreamError` reads both.

A host-capability failure is refused before any of that, since a different host may serve it fine. The order earns itself in both directions: a `type: server_error` whose message reads "boom" is refused on its class, where the message alone said nothing, and a 400 whose message happens to contain the word "timeout" is stored on its status rather than refused on a coincidence.

The polarity is a blacklist: an error naming nothing recognisable is stored. Refusing it instead would send every repeat of a permanently broken request back to the hosts, which is the worse failure of the two — but it is the half of this rule that a host naming something unrecognised breaks first, and the evidence that would justify a whitelist is what the hosts actually name.

The engine reads these rules through `IsCacheableUpstreamError` to stop escalating on a trusted host's 400, so changing what they accept, their polarity included, moves the race's escalation with the cache (see [`docs/race.md`](../docs/race.md), "Escalation").

`parseUpstreamErrorDetails` reads the error from plain JSON or from inside an SSE data event, and `DecodeUpstreamError` accepts both the nested `{"error":{...}}` shape and the flat `{"object":"error",...}` one vLLM still emits. The scan visits every event rather than stopping at the first decodable one: an empty `{"error":{}}` decodes while carrying nothing, so stopping there would leave a real error in a later event unseen — and a choice's terminal reason usually arrives after the events that carried its content. The first failure found is the one kept. A null `code` renders as absent rather than the literal text `<nil>`.

### Finishing an answer

The gateway writes its own `[DONE]` (`api/stream.go`, `terminateLocked`), so the terminator a client reads says nothing about whether the model finished. The race fails an answer its host closed with neither `[DONE]` nor a finished nonce (`engine/failure.go`, `ErrAnswerTruncated`), but a host that finished its nonce without a terminator, or sent one before its choices ended, is still served as a 200. The only signal left is a terminal reason on every choice the reply started, and storing a reply without one replays that unfinished body to every retry of the same request for as long as the entry lives.

- `finish_reason` and `stop_reason` both end a choice, the way `completionAsChunks` ends one; `null`, `""` and absent are all still running. Both stay raw through the decode, because a wrong type in one of them would otherwise fail the decode that also finds the error.
- Any reason a host names is terminal, `"length"` and a reason no spec lists included: the question is whether generation ended, not why. A reply cut short by `max_tokens` ended.
- A choice is remembered by its index, so a finished choice cannot vouch for an unfinished sibling. `n` is forced to 1 (see "Parameter rules"), so this is not for a client that asked for several — it is for a host that answers with more choices than it was asked for. A choice whose index is missing or unreadable is remembered by where it stands in the list, which is how a client reads it.
- A reply that started no choice counts as finished. An answer carrying nothing is the engine's to fault (see [`engine/README.md`](../engine/README.md), "Classification and reassembly"), and refusing it here would also refuse a body that is a bare error object.
- Past `maxIndexedElements` distinct choices the reply is refused whatever its reasons say. That is the bound the fold already applies to the same input, and a host naming choices without limit is not one to replay.

A recorded host stream that finishes still earns its entry: `TestEveryRecordedStreamIsClassified` names the verdict for every fixture in `testdata/sse`, so a new one fails the suite until somebody says which side of the gate it belongs on.

Reading the choices is what the answer costs: the walk is about 1.6 times the price of asking for the error alone (`BenchmarkIsCacheableResponse`). It scales with the body, so a 1.3 MB reply pays about a millisecond of it on the store — against the seconds that same reply spends being written to the client. Only the store pays: the read path asks the failure alone. The events-side alternative, counting inside the rewriter and the fold as the events go past, costs a tenth of that, but it answers from writer state rather than from the bytes actually stored, and it cannot see a body that was never framed as events at all.

`DecodeUpstreamError` skips the decode for a payload that cannot carry an error, which is almost every event of a stream: the classifier asks it of each one. `mayCarryUpstreamError` walks the keys the way the rewrite's fast path does, unescaping and ignoring case. A key `error`, or an `object` key whose value is not a plain unescaped string other than `error`, sends the payload to the decode; anything it cannot read does too. `FuzzErrorPreCheckAgreesWithTheDecode` holds it to skipping only payloads the decode would answer with no error.

### Capability errors

`CapabilityLimits` parses vLLM's context-window refusal for the host's limit and the tokens the request needed, and `ToolChoiceUnsupportedMessage` is the tool-choice refusal verbatim. These phrases are matched here and nowhere else.

The limit always follows `maximum context length is `. The tokens needed are read from whichever wording the host's vLLM release writes, in this order:

- v0.25.1 and later (`vllm/renderers/params.py`): `for a total of {total} tokens`, with `at least ` before the number when the tokenizer cut the prompt short. The number after `you requested ` in this wording counts the output tokens alone, so it is never read when a total is present.
- v0.25.1 and later, the character pre-check: `you requested {output} output tokens and your prompt contains {characters} characters (more than {bound} characters, which is the upper bound for {input} input tokens)`. The prompt is longer than `{input}` tokens, so the request needs at least `{input} + {output} + 1`.
- v0.11 (`vllm/entrypoints/openai/serving_engine.py`): `your request has {input} input tokens`, plus `is too large: {output}` when `max_tokens` is what overflowed.
- v0.9.1 (same file): `you requested {total} tokens`.

A lower bound is enough for the race: it only asks whether the request could fit the model's length ([`docs/race.md`](../docs/race.md), "Escalation").

`uintAfterPhrase` runs both the search and the slice on the lowered copy of the message. Lowercasing can shorten a string — U+212A KELVIN SIGN lowers to a one-byte `k` — so an index taken from one string and applied to the other lands mid-word.

## SSE framing

### Two `data:` prefixes that must not be unified

`sseDataPrefix` (`"data: "`, with the space) is what the gateway *emits*. `sseDataParsePrefix` (`"data:"`, without) is what it *parses*, because a host may send either. They look like a duplicate and are not.

### Reading events

`indexEventEnd` returns the offset just past the first `\n\n` or `\r\n\r\n` terminator. It walks line by line rather than searching for both separators: searching for a CRLF terminator that an LF-framed stream never carries would scan to the end of the buffer for every event.

`eventPayload` joins an event's `data:` lines with a newline, the way a client does per the SSE spec, and reports where those lines were. One object split across two data lines reaches the client as one object, so it must reach the strip as one too.

`rebuildEvent` writes the rewritten payload back, keeping every non-data line where it was — a client reads `event`, `id` and `retry` from the lines around the data. The payload goes out as one `data:` line per segment, the inverse of the join: written after a single prefix, its embedded newlines would start lines carrying no `data:` prefix, and a client drops those and rejoins a truncated object.

`MaxStreamCarryBytes` (32 MiB) bounds the unterminated tail held per stream, so a host that never sends a terminator cannot grow it without limit. Both `StreamRewriter` and `BodyFolder` fail permanently once it is exceeded.

`ErrStreamTruncatedEvent` reports a trailing partial event dropped at the end of the stream: `StreamRewriter.Close` rewrites a well-formed trailing partial and drops one that does not parse.

### Rewriting an event

`rewriteEvent` forwards only what the decoded payload says the client may see. A host controls the event's raw bytes: it can spell a key with a `\u` escape, repeat a key, or split one object across two data lines, and each defeats a naive byte-wise check while the client's own decoder reads the object whole. The two byte-level paths below are held to the decoded answer and decline every one of those spellings.

Most events need no decision at all: a host strips the logprob family before sending when the client asked for none, so a plain content chunk carries nothing to remove. `carriesNothingToRewrite` proves that without decoding, for a client that asked for no logprobs and an event on one data line. It walks the payload's keys at every depth, unescapes a key that carries a `\` escape before comparing it, compares without regard to case, and only then checks that the whole payload is one valid object. Any key in `clientStrippedFields`, `message`, or `usage` when the client did not ask for it sends the event through the full decode. So does anything that fails the check. A spelling the check might misread costs a decode, never a leak, and `FuzzFastPathAgreesWithTheFullRewrite` holds it to forwarding only what the full rewrite would forward byte for byte.

### Stripping without decoding

A host forwards the logprob family to a client that asked for logprobs, so every one-token chunk carries several hundred bytes of it. The rewriter still has work on such a chunk, removing what the client must not see and emptying `top_logprobs` when it asked for logprobs without alternatives, and decoding the chunk into `map[string]any` for that, deleting fields and encoding it again was its largest cost per chunk.

`stripMembersWithoutDecoding` (`strip_members.go`) applies `stripDecodedFields` to the bytes instead: it copies the payload member by member, skips every member the intent hides, and writes `[]` for `top_logprobs` when the client kept logprobs but not their alternatives. It runs only on a payload `encoding/json` accepts whole, and declines a key carrying a `\` escape and a key repeated within one object, the two spellings whose decoded reading differs from their bytes; a declined payload takes the decoding path. The output keeps the host's key order and number spellings where the decoding path re-encodes them sorted, so the bytes differ while every decoder reads the same value.

The rewriter uses it for an event on one data line unless the event carries `usage` it must drop or a `message` it must convert, both of which reshape the event (`stripWithoutDecoding`).

`TestStrippingWithoutDecodingMatchesDecoding` and `FuzzStrippingWithoutDecodingMatchesDecoding` hold it, for every logprob intent, to decoding to exactly what decoding and `stripDecodedFields` produce and to reporting a change exactly when they change the value. `BenchmarkStreamRewriterStandStream` measures it on a stand-shaped stream for every intent: one decode and one encode per chunk become one validation and one copy.

A payload that opens as an object and does not parse is dropped rather than forwarded — a host sending something no client can read would otherwise carry along whatever it hides. A payload that is not object-shaped passes through untouched.

When the client did not ask for usage, `dropUsage` removes it. An event left with nothing but housekeeping (`id`, `object`, `created`, `model`, `system_fingerprint`, `service_tier`, and an empty `choices`) is dropped entirely. The test is for housekeeping rather than for empty choices, because a host's error event carries no choices either and must survive.

### A complete reply on a streaming request is rewritten into chunks

Some hosts answer a forced stream with a whole `chat.completion`. That hands the client a `message` where it reads a `delta`, so the client renders nothing while the nonce is settled and the money is spent. `completionAsChunks` converts it into the `chat.completion.chunk` events an OpenAI client actually renders, sending the role, the payload and the finish reason as separate chunks the way a real stream does, with usage last. Since the whole answer arrives as one delta, its logprobs ride that same chunk. The conversion decodes the event again, so it runs only for a choice carrying a `message`; a streamed chunk carries a `delta`.

Every host-controlled field is carried as a raw message. Decoding one into a typed field would let a host fail the conversion with a value of the wrong type — a numeric `id`, a `created` past the float range — and the client would be back to reading a message where it renders a delta. `rawOr` supplies a fallback for an absent field, since an empty raw message is not JSON and would fail the very encode this conversion exists to produce; the fallbacks are the zero values a typed field encodes, so an ordinary response converts byte for byte. `presentValue` counts JSON `null` as absent, so a field the host spelled out as null is not re-sent as one the client must interpret.

`SSEDoneEvent` is the terminator an SSE client reads until; without it the client waits out its own timeout instead of finishing. `HasSSEDone` checks whether the stream already carries one, line-anchored so a `[DONE]` inside a content delta is not mistaken for the terminator.

## Folding a stream into one body

`BodyFolder` is the incremental fold: it strips per event as chunks arrive, then merges, so a client that did not ask for logprobs never accumulates them. `assembleSSEBody` in `assemble.go` is the whole-body fold, the reference implementation the incremental one is verified against. Both share the merge, and both hold a host to the same bounds it must not exceed: `maxIndexedElements` (256), `maxTopLevelFields` (64) and `maxAssembledEvents` (65,536), each described below.

`Held()` reports what the folder is holding so a shared memory budget can watch it. Merging collapses what the events repeat, so the accumulated size is *measured* rather than summed. The trigger for a re-measure is bytes (`foldMeasureBytes`, 256 KiB) rather than events: one event can carry a megabyte, and a count-based interval would leave that much unaccounted while the cap and the shared budget read low. `measure` re-encodes the accumulator without finalising it, because finalising rewrites the deltas the fold is still appending to.

### The content template

A host restates every chunk whole, so consecutive plain chunks differ only in their content text. After each full merge the folder keeps the chunk's bytes on either side of its content literal (`fold_template.go`), and a later event that carries the same bytes around a single string literal appends that text to the merged delta without being decoded. That merge is the one the full path would have run: every other field restates what the merge already holds.

The template is kept only when that holds: one choice with a numeric index, no accumulating field on it but `delta`, a delta of strings whose only growing text is `content`, no other growing text field that is not null, exactly one `content` key in the payload, escaped spellings included, and room left under `maxIndexedElements`. Anything else, a literal with escapes the standard decoder reads differently included, falls back to the full decode, so the template only skips work. `FuzzContentTemplateAgreesWithTheFullMerge` pins that against the same folder with the template cleared before every event.

On a plain 512-chunk stream the fold drops from about 1.7 ms and 1.36 MB to about 83 µs and 42 KB (`BenchmarkBodyFolderPlainContentStream`). A host strips the logprob family before sending unless the client asked for it or its logprobs optimization is off (`common/completionapi/responseprocessor.go`, `prepareBody`).

A client that asked for logprobs receives them on every chunk, so the template also covers a chunk whose choice carries `logprobs` holding nothing but a `content` array: the payload then has exactly two `content` keys, the delta's string and, after it, that array, and the template keeps the bytes before the string, between the two, and after the array. It forms only once the merged entries are non-empty and do not lead with an `index`: the full merge appends entries after such a first one, and merges indexed entries into one another, which raw entries cannot join. A matching event appends its text as above and its entries to the merged `logprobs.content` as raw JSON, after `stripMembersWithoutDecoding` applies the client's intent to each, without decoding them (`canonicalEntries` in `fold_logprobs_entries.go`). An entry is kept raw only when it is spelled as `encodeCompact` would spell its decoded value — no whitespace, keys in ascending order, strings without escapes, control bytes or the two line separators it always escapes — so the folded body stays byte for byte what the full merge encodes; any other entry sends the event through the full decode. `TestLogprobsTemplateFoldsWhatTheFullMergeWould` and `FuzzContentTemplateAgreesWithTheFullMerge` pin that for every logprob intent. `BenchmarkBodyFolderStandStream` measures it on a stand-shaped stream for every intent: a chunk's decode becomes a scan and a copy of its entries, while the periodic `measure` re-encode, which still encodes every raw entry, makes a long answer cost more per chunk.

### What the fold returns

- A failed carry-overflow, or a stream that framed events but carried no payload, returns `NoResponseDataBody`.
- A fold that ran past `maxAssembledEvents` (65,536) returns `TruncatedResponseBody`. Returning the prefix would be a complete-looking answer missing its tail.
- A complete `chat.completion` seen before any chunk was merged is returned stripped, as-is.
- Otherwise the merged accumulator is finalised and encoded. Finalising turns each choice's `delta` into its `message` and gives it the shape a non-streamed OpenAI answer has (`finalizeMessage`): `role` defaults to `assistant`, an empty `tool_calls` list is dropped and each entry loses its streaming `index`, `content` becomes `null` beside tool calls when it is empty or absent and `""` when absent without them. A document holding nothing but an `error` keeps its own shape and is not relabelled `chat.completion`.
- A stream that never framed a `data:` line at all is returned as the raw body, stripped — an unframed error page still reaches the caller.

### How the merge works

Everything outside `choices` is a restated header, so it replaces rather than accumulates, bounded by `maxTopLevelFields` (64) for keys not already present. Within a choice, only `delta`, `logprobs` and `token_ids` accumulate; `finish_reason` and the rest replace.

`mergeStreamedValue` accumulates by *field name*, never by Go type. A host restates its identity fields, and growing those hands the client a tool call it cannot answer. Only `content`, `reasoning`, `reasoning_content`, `refusal` and `arguments` grow as text.

Text grows through `growingText`, which keeps the join off the per-chunk path where it would be quadratic in the answer's length. `growText` appends, but replaces instead when a host re-sends `arguments` whole — detected by the incoming string having the accumulated one as its prefix.

Arrays merge by `index` when the accumulated array leads with an indexed element and the incoming one is fully indexed; otherwise they append. `leadsWithAnIndex` decides on the first element alone: scanning the whole array per chunk makes the merge quadratic. Indexed merging is bounded by `maxIndexedElements` (256).

`finalizeCompletion` rewrites the accumulator in place — so it runs once, at the end, never as part of measuring — turning each choice's `delta` into its `message`, ordered by index. A message with no `content` key gets an explicit `null`, because upstream answers a tool call with a null content rather than with the field absent.

## Errors and rejection status

`RejectError` carries an HTTP status and wraps a cause. `Reject` and `WrapReject` both produce a 400. `ErrorStatus` checks for an ingest-cap overrun first: a body refused for its size stays a 413 even when a `RejectError` carrying 400 wraps it.

## Read next

- [`docs/request.md`](../docs/request.md) — every rule, every stage, and the arithmetic behind the bounds.
