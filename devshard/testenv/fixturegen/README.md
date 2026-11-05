# Honest vLLM answer fixtures

A fixture is what one real vLLM build would have streamed and counted for one chat
request answered with a known assistant turn. It is built without a GPU, weights or
network: the assistant turn is rendered with the model's own chat template, tokenized
with the model's tokenizer, and the token ids are fed one engine step at a time through
vLLM's own `OutputProcessor` (incremental detokenizer, stop strings, logprobs processor)
and the `OpenAIServingChat` stream and non-stream generators (reasoning and tool parsers,
logprobs formatting, usage). A `vllm launch render` server with the model's serving
arguments supplies the prompt token ids and the host-side counts.

Only the logprob *values* and the top-k *alternative ids* are synthetic (sampled token is
rank 1 with logprob -0.1, alternative r has logprob -0.1-r and id `(sampled+r) mod vocab`).
Token strings, `bytes`, chunk boundaries, delta fields, finish reasons and usage come from
vLLM code.

## Files

- `cases.json` - requests and the assistant turns to produce (`reasoning`, `content`,
  `tool_calls`), optional `max_tokens`, `stop`, and `cut.after_text` (cut by `max_tokens`
  right after the token that completes that text). `content` may be a list of strings and
  `{"repeat": s, "times": n}` parts. `answer_by_model` overrides parts of the answer for one model (MiniMax always opens with reasoning).
- `models-<build>.tsv` - model, pinned revision, image, tool-call tolerance, default request mode,
  serving arguments; `-glm` tables hold the GLM row of a build that needs its own image.
- `build/` - Dockerfiles of the render images and `docker-bake.hcl`, which builds them (see "Images").
- `gen.py` - the generator; runs inside the image next to the render server.
- `run.sh [model-substring] [case ...]` - one container per model (`--network none`,
  `--memory 8g`), then `check.py`.
- `check.py <dir>` - re-verifies fixtures from JSON only and prints a table.
- `fixtures/<model>/<case>.json` - fixtures; `<case>.error.json` when a case could not be
  built for that model (error and traceback); `<case>.rejected.json` when the gateway would
  refuse the request (see "Forwarded request"); `summary.json` per model.

## Fixture fields

| field | meaning |
|---|---|
| `build` | model, revision, image tag and id, vLLM / transformers / tokenizers versions, serving args, tokenizer and detokenizer classes, parser class, eos ids, sha256 of every non-weight file in the model snapshot and a combined `tokenizer_sha256`, chat template sha256 |
| `request` | body as the ML node receives it; in the variant directories this is the client body passed through the gateway and host rewrites (see "Forwarded request"); `notes.request` holds the client body, the gateway sha, assumed gateway settings and every applied rewrite with its source lines |
| `answer` | the intended assistant turn; `cut` the cut spec, if any |
| `engine` | prompt length, effective `max_tokens`, `stop`, eos and stop token ids, the stop token that closes the turn (`terminator`), render-server sampling params |
| `token_ids` | every generated id, including the stop token on a natural finish (generated, counted, not detokenized) and the tokens of a matched stop string |
| `tokens` | per position: `id`, `token` and `bytes` exactly as the logprobs entry carries them, `text` = bytes decoded |
| `stream` | the SSE `data:` objects in order, `[DONE]` omitted, `created` set to 0 |
| `response_nonstream` | the same answer through the non-streaming generator |
| `finish_reason`, `stop_reason`, `usage` | as streamed (a `max_tokens` cut after a tool call started is streamed as `tool_calls`) |
| `engine_finish_reason`, `engine_stop_reason` | what the engine reported (`length` for any `max_tokens` cut; stop token id or stop string) |
| `served` | concatenated stream deltas: `reasoning`, `content`, `tool_calls` (index, id, name, arguments) |
| `host` | the generator's own count of the answer, which replay keeps as the build's render counts (`rendered_input`, `rendered_output`; the voters' rule is `devshard/host/challenge_usage.go`, which counts served arguments as served and leaves cut calls out): tool-call arguments repaired (`partial_json_parser`, `Allow.ALL`, non-object becomes `{}`), then `rendered_output` = render of request + repaired served turn minus request + empty assistant turn; `rendered_output_unrepaired` kept for the record. On a `max_tokens` cut inside a tool call, `cut_location` names the open argument and its schema type; a non-`string` type gives `renderer_abstains` with `abstain_reason: non_string_arg_cut`, a render that still fails gives `render_error_after_repair`. Also `positions` (logprobs entries), `rendered_input` (render with `add_generation_prompt`), `claim` (= `completion_tokens`), `gap` = claim - rendered_output, `tolerance` = 8 + tokens of the longest stop string + per-model tool-call allowance |
| `gateway_bytes` | UTF-8 bytes of delta `content`, `reasoning`, tool-call `name` and `arguments` |
| `checks` | self-checks run by `gen.py`; `kind: finding` marks vLLM behaviour worth reporting rather than a fixture defect |

Tool-call ids (`chatcmpl-tool-...`) are random per run, as in vLLM.

## Build binding

A fixture is valid only for the build recorded in `build`: same image id, vLLM version,
model revision and `tokenizer_sha256`. Upstream `vllm/vllm-openai-cpu:v0.28.0` writes the
decoded text into logprobs `token`; the Gonka fork writes the decimal token id there
(`bytes` stays the decoded text for the sampled token and the id string for alternatives).
Upstream and the 4.0.0 fork add `usage.completion_tokens_details.reasoning_tokens`; the 3.1.0 and 3.0.16 forks do not.
Regenerate after any change of image, revision or serving arguments.

## Variants

Each fixtures directory is one vLLM build. Image variants were identified from the ML node images'
vLLM layers, matched file by file against gonka-ai/vllm.

| directory | label | image | models | ML node versions |
|---|---|---|---|---|
| `fixtures-mlnode310/` | mlnode-3.1.0 (vLLM fork 71ee49c4d) | `local/vllm-cpu-glm53:71ee49c-render` (CPU wheel of the fork; launch.py patched for the render server's CacheConfig) | all three (GLM via `models-mlnode310-glm.tsv`) | 3.1.0, 3.1.0-post3 |
| `fixtures-mlnode3016/` | mlnode-3.0.16 (vLLM 0.25.1 + fork be6059424) | `local/vllm-cpu-mlnode3016:be6059424` (`build/Dockerfile.overlay`) | DeepSeek, MiniMax (GLM-5.3 does not start: Transformers in this build does not know model type `glm5_next`) | 3.0.16 (image overlay matched as 6e9684a17, identical .py tree) |
| `fixtures-mlnode400/` | mlnode-4.0.0 (vLLM 0.30.0 + fork 8219a3e2e) | `local/vllm-cpu-mlnode400:8219a3e2e` (`build/Dockerfile.overlay400`, with gonka-poc v0.2.0 because this fork's API app imports it); GLM on `:8219a3e2e-render` (`build/Dockerfile.render400`, render server CacheConfig only) | all three (`models-mlnode400.tsv`, GLM via `models-mlnode400-glm.tsv`) | 4.0.0-sigma1, -cu12, -minimaxxer; 3.1.0-post3-pfrun0 (fork 88a2ebf3b, differs only in `v1/core/sched/scheduler.py`) |

Overlay images copy the fork's `*.py` files over the stock CPU build of the same vLLM version, with the
fork's own version check (`docker/Dockerfile.gonka-poc` recipe); the gonka-poc plugin is not installed,
it does not touch the chat serving path in 0.25.1; in the 0.30 fork the API app builder imports it, so the 4.0.0 image installs it as the fork's recipe does.

Not reproducible (no image in the registry or no source): ML node versions 3.0.14, 3.0.14-post2,
0.2.0, 0.1.0, 3.0.16-spark-pilot, escrow_mining_v3, custom `*+vllm0.30.0.poc0.2.0.*` builds, and
nodes reporting an empty version.

DeepSeek-V4-Flash runs every case in three request modes: default (no `chat_template_kwargs`),
`@thinking` (`{"thinking": true}`) and `@chat` (`{"thinking": false}`). `thinking` is the kwarg the
vLLM DeepSeek-V4 tokenizer's `apply_chat_template` reads. Its default differs by build: thinking in
0.28 and 71ee49c, chat in 0.25.1 (column `default_mode` in the models table). In chat mode the
expected answer has no reasoning (`modes` in `cases.json`).

```
MODELS=models-mlnode3016.tsv OUT=fixtures-mlnode3016 LOGTAG=mlnode3016 \
  VARIANT="mlnode-3.0.16 (vLLM 0.25.1 + fork be6059424)" BUILD_NOTE="..." ./run.sh
```

## Images

```
docker buildx bake -f build/docker-bake.hcl            # all four images
docker buildx bake -f build/docker-bake.hcl mlnode310  # one variant
```

Run from this directory. Each image is built from gonka-ai/vllm at the commit pinned in
`build/docker-bake.hcl`. The 3.1.0 image installs the CPU wheel made by the `vllm-build` stage of the
fork's own `docker/Dockerfile.cpu` (about 15 minutes with `MAX_JOBS=8`); the overlay images take the
fork checkout as the `fork` build context. Tags are the ones in the models tables. `IMAGE_PREFIX`
replaces their `local/` prefix, and the tables then need the same change.

## Forwarded request

`gen.py` builds the client body of a case, then `forward_request` applies what gonka-ai/gonka
`af2618b5acbe22df75d1c6f899fbbc8b96998e8c` (branch `devshard-0.2.x-v6`) does before the ML node sees it:
the gateway parameter catalog and output token limits (`devshard/cmd/devshardctl/request_filters*.go`,
`paramvalidators/`), then the host executor's `completionapi.ModifyRequestBodyWithLogprobsMode`. For these
cases that means: `tool_choice: "auto"` added next to `tools`; `max_tokens` and `max_completion_tokens`
capped at 4096 (default `request_max_tokens_cap`); GLM-5.3 gets `chat_template_kwargs.enable_thinking: true`;
`logprobs_mode: "processed_logprobs"` (chain default); object keys sorted at every level, because both
the gateway and the host re-encode the body from a Go map. A request the gateway would refuse is written as
`<case>.rejected.json` with the rule instead of a fixture.

The template does not see the sorted tools as sent: vLLM passes `tool.model_dump()` of its pydantic
models, so `type`/`function`/`name`/`description` come back in model field order while `parameters`
keeps the sorted order. The local template render does the same.

## Special cases

- `forced_fragment`: the request carries `logit_bias {id: 100}` and `max_tokens` 256 for a token that is one
  byte and decodes alone to U+FFFD (DeepSeek 159, MiniMax 224, GLM 94; checked in each build's tokenizer and
  recorded in `case_info`). The honest generation is that id 256 times. The fixture records the real stream
  (256 positions, finish `length`) and the render count; the out-of-tolerance gap is reported as the finding
  `client-forced fragments render to nothing`, not as a failure.
- `utf8_tail_cut`: the answer ends with the first candidate character the tokenizer splits into byte tokens
  (`case_info.utf8_tail`: character, ids, pieces), and `max_tokens` stops after the first 1-3 of them. The
  check `utf8_tail_dropped` expects exactly those positions at the end of the stream with empty deltas.

## Regenerate

```
./run.sh                      # all models, all cases
MODELS=models-mlnode310-glm.tsv ./run.sh GLM tool_one  # one model, selected cases
python3 check.py              # defaults to ./fixtures
```

Needs the images listed in the models table and an offline HF cache with the tokenizer files,
passed as `HF_CACHE`.

The mock ML node replays one file per build, `../mockopenai/testdata/vllm_recorded_<build>.jsonl.gz`,
written from a fixtures directory by `../cmd/replayfixture` (from `devshard/`). The mock keeps one answer per
request key, so cases that would reach the ML node as the same request (the `split_bytes_*` fractions,
`guided_json_split_100`, and `tool_choice_required`, which the gateway forwards as `auto`) ask with prompts of
their own:

```
go run ./testenv/cmd/replayfixture -variant "mlnode-3.1.0 (vLLM fork 71ee49c4d)" \
  -out testenv/mockopenai/testdata/vllm_recorded_mlnode310.jsonl.gz \
  $(ls testenv/fixturegen/fixtures-mlnode310/*/*.json | grep -v -e summary -e error -e rejected)
```
