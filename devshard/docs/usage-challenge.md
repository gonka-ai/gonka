# Usage challenge: byte bounds and counted votes

A Finish claims `input_tokens` and `output_tokens`. The gateway challenges a
claim it cannot believe, the hosts other than the executor fetch the stored
answer and vote, and votes above the threshold invalidate the inference (the
escrow gets the cost back, the executor gets an invalid count). The state
machine tallies faults and does not read which check found them.

## Gateway: byte bounds

When the executor's Finish is bound to the answer the gateway relayed, the
gateway knows the text bytes the client received. A token decodes to at least one byte once its character is complete,
so the bytes bound the tokens:

- output: `min(max_tokens, 2 × (text_bytes + service + longest_stop + tool_calls × tool_header + arguments × argument_header))`,
  where `arguments` counts the top-level arguments of the calls;
- input, for text-only requests: `2 × (request_bytes + prompt_template)`.

Text bytes are choice 0's delta `content`, reasoning (`reasoning` when it
carries text, else `reasoning_content`) and each tool call's name and
arguments; a whole `message` in a chunk is not text a streaming client reads
and is not counted. A
claim above a bound is challenged with reason `output_above_bytes` or
`input_above_bytes`. The stream reasons (`stream_unbound`, `empty_stream`) take
precedence, and an inference is challenged once. An answer the gateway did not
ask the ML node to stream is judged on its input only. A bound stream whose
text the gateway did not count whole (its scan buffer dropped text, or the
prelude ran past its limit) has no byte bound: its claim is challenged with
reason `unmeasured`, and the voters count it.

The per-model constants live in `types/usage_allowance.go`. A model not in that
table gets `max_tokens` as its output bound and no input bound.

The one honest answer known to pass the output bound is a client forcing a
single-byte fragment token with `logit_bias` +100: the fragments decode to no
text. The gateway rejects `logit_bias` (see "Token bans and biases"), so no
such answer reaches the network.

## Voters: counted checks

A voter that fetched a body whose hashes match counts it, whatever reason the
gateway gave:

1. **Positions.** `output_tokens` above the stored body's generated positions
   (choice 0 `logprobs.content`) plus 3 is `USAGE_INFLATED` with check
   `positions`. No positions at all is `EMPTY_STREAM`, except for a model whose
   answer can come back empty when vLLM strips the only tokens it generated
   (`types.ModelBurnsEmpty`): with output tokens claimed, its empty body is
   `NONE`. A body of that model with positions, or one that cannot be read, is
   counted like any other. A streamed body is read as the gateway reads it:
   each delta, skipping an event that is not a readable chunk; a `message` in
   a chunk adds positions but no text.
2. **Rendered output.** The served turn is appended to the executed request and
   rendered by the model's render server, less the same request with an empty
   assistant turn. The claim is inflated (`rendered_output`) above
   `min_tokens + 2 × rendered + service + longest_stop + tool_calls × tool_header`.
   Up to 3 trailing positions without text of an answer cut by `max_tokens` are
   left out of the claim: a cut can leave a character unfinished, its bytes
   generated and never served. A tool call before the last one whose arguments
   are not a whole JSON object is inflated (`rendered_output`) with no render:
   only the last call can be cut.
3. **Rendered input.** The executed request is rendered with its generation
   prompt; `input_tokens` above that count plus the model's spread between
   honest builds is inflated (`rendered_input`).

An answer that carried reasoning was generated in the model's thinking mode.
When the request does not name the mode, the voter names it in both renders
for a model whose builds default to different modes (`ThinkingKwarg` in the
table): DeepSeek-V4 reasons by default on 3.1.0 and 4.0.0 and answers in chat
mode on 3.0.16, whose template renders a turn in chat mode without its
reasoning. A request that names the mode is rendered as it was executed.

`deviation` in the vote is the claim less the count of the check that fired.
The executed request is rebuilt from the prompt payload whose hash the Finish
signed, as the executor built the body it sent to its ML node.

A rule of the request, the same for every voter, leaves a rendered check out,
and the vote then rests on the checks that remain:

- a grammar (`response_format` of type `json_object`, `json_schema` or
  `structural_tag`, `structured_outputs`, `tool_choice` `required` or naming a
  function): no rendered output;
- the last tool call cut by `max_tokens` (the claim is `max_tokens`) inside an
  argument the request's schema does not type as a string, or whose type the
  voter cannot tell: no rendered output. The tool parser holds such an argument
  back until it is whole, so what the engine generated for it was never served.
  A call cut inside a string argument is served as generated and is rendered
  as served;
- a message with a non-text part: no rendered input;
- a model outside the table, or no executed request (the prompt payload could
  not be rebuilt or read): neither.

If the ML node refuses the body (HTTP 400 or 422), the voter tries the
configured render servers. If a standalone server refuses the first render,
the voter stops trying servers and does not vote (`no_render`). If the second
render of a turn fails, the voter retries both renders on the other configured
servers.

The output check's factor of 2 holds for answers close to the tokenizer's own
spelling. A sampler that writes half or more of the multi-byte characters as
single-byte tokens runs past it (2.5 to 4.5 times the render in the recorded
fixtures), and the voters would call such an answer inflated.

## Vote delivery

Each escrow has one background delivery loop. It queries the participants when
a dispute opens, then waits 1, 2, and 5 seconds between rounds without progress,
staying at 5 seconds. A change to a dispute's status or vote totals resets the
delay to 1 second without postponing an earlier scheduled poll. Ordinary
Start/Finish traffic, duplicate messages (including another slot of the same
participant), rejected votes, and validations of unrelated work do not reset
the poll timer.

Ready votes from a host response are composed and delivered immediately through
the usual signed diffs; waking that delivery does not start another query round.
Responses from fast participants are processed while slower queries are still
pending. An empty round spends no nonce. Finalize takes control of delivery,
Close cancels outstanding queries, and the existing balance and finalization
nonce reserve still apply.

## Token bans and biases

A request that bans tokens is what pushes an honest model past the factor of
2: the banned tokens have to be spelled from smaller ones. A positive bias on
a byte fragment passes the gateway's byte bound with tokens that decode to no
text. Restricting the sampler with `allowed_token_ids` has the same effect.
The gateway rejects `bad_words` and non-empty `logit_bias` or `allowed_token_ids`
with HTTP 400; empty controls are dropped. An executor drops all three fields
from the body it sends to its ML node, a validator from the replay, and a voter
from the request it renders, after the signature and the prompt hash
are checked on the payload as fetched. The gateway also rejects a non-null
`truncate_prompt_tokens`, and the same three paths drop it: truncation can make
both render requests for a turn return the same length, hiding the answer. Penalties for repeated tokens
(`frequency_penalty`, `presence_penalty`, `repetition_penalty`) ban nothing
and pass.

## Render

A voter counts with the render route of vLLM's chat serving,
`/v1/chat/completions/render`: the model's chat template and tokenizer, without
running the model. It asks its own ML node first, through the same node pick
as an inference, then the render servers named in `DEVSHARD_RENDER_URLS`, in
turn.

The ML node render uses the voter's vLLM build, which may differ from the
executor's build. An independently configured render server may also use a
different build. vLLM 0.30 and later register the ML node route only
with `--enable-scale-out`. The ML node's runner adds the flag when the
installed vLLM is 0.30 or later and the model arguments name neither
`--enable-scale-out` nor `--no-enable-scale-out`; on a runner without that
change, add `--enable-scale-out` to the model's local `args`. An ML node that
answers 404 on the route is logged once per model, and its counts go to the
render servers.

An ML node busy with PoC has no free node to count on. When no render server
counted either, the voter counts the same fetched body again until the end of
the challenge window, and answers `render_busy` if no node came free.

A render server is `vllm launch render`, which runs the model's chat template
and tokenizer on CPU, without weights or a GPU. Start it with the same model,
revision and serving arguments as the ML node (tool and reasoning parsers,
tokenizer mode):

```
vllm launch render deepseek-ai/DeepSeek-V4-Flash-0731 --revision <rev> \
  --max-model-len 400000 --kv-cache-dtype fp8 --tokenizer-mode deepseek_v4 \
  --enable-auto-tool-choice --tool-call-parser deepseek_v4 \
  --reasoning-parser deepseek_v4 --trust-remote-code --port 8000
```

The upstream CPU images of vLLM 0.30 and 0.32 count the recorded DeepSeek-V4
and MiniMax answers of every ML node build as the 4.0.0 build's own render
does. GLM-5.3-Flash does not start on them: the render launcher builds its
config with prefix caching on, the default, the hybrid model config sizes its
cache blocks for prefix caching, the CPU platform turns prefix caching off
afterwards for an MLA model on a CPU without AMX, and the config validator
rejects the result. With the launcher fixed, 0.32 counts GLM-5.3-Flash as the
4.0.0 build does. `Dockerfile.render` builds the 0.32
image with the launcher passing `CacheConfig(enable_prefix_caching=False)`;
`deploy/join/docker-compose.render.yml` runs it, one service per model. The
`-render` fixture images (`testenv/fixturegen/build/Dockerfile.render310`,
`Dockerfile.render400`) carry the same change.

devshardd reads the servers from `DEVSHARD_RENDER_URLS`:

```
DEVSHARD_RENDER_URLS="deepseek-ai/DeepSeek-V4-Flash-0731=http://render-ds:8000|http://render-ds-2:8000,zai-org/GLM-5.3-Flash=http://render-glm:8000"
```

Several servers for one model share its counts in turn; a server failure
other than a refusal moves the first render to the next one. If the second turn render fails, the voter
repeats both renders on a backup so the subtraction uses one server. A 200
response without a usable `token_ids` array is treated as a failed render and
the next server is tried. Without a working server, the voter does not vote. The vote
log lists the selected ML node IDs and endpoints for the output and input counts;
URL credentials and query parameters are omitted. At start devshardd logs, for each table
model, the ML node route and the render servers it will ask.

A voter that cannot count does not vote on a challenge that needs a rendered
count: it answers with reject cause `no_render`, and its vote is missing from
the tally. It still votes where no render is needed: a hash mismatch, a missing
payload, a body short of its claim on positions, or a request whose rules leave
both rendered checks out. The gateway counts the reject causes per voter, and
with `DEVSHARD_GATEWAY_NO_RENDER_STRIKE=true` a `no_render` answer is a failure
strike in the participant quarantine, as a refused escrow lookup is.
`render_busy` is never a strike.

## A new model

The allowance of a model is measured on answers recorded through vLLM's own
serving code on every ML node build that serves it:

1. Add the model, its pinned revision, the build's image and the ML node's
   serving arguments to each `testenv/fixturegen/models-<build>.tsv`, and
   record the cases with `run.sh` (see `testenv/fixturegen/README.md`).
2. Write each build's replay file with `cmd/replayfixture`.
3. Add the model to `types/usage_allowance.go` and set the constants from the
   fixtures' `host` and `usage` fields:
   - `ServiceTokens`: at least the largest `claim - rendered_output` of an answer with
     no tool call that finished on its own;
   - `ToolCallHeader`: at least the largest further gap per tool call of the answers
     with tool calls;
   - `ArgumentHeader`: half the tokens per argument past twice its bytes, for a
     call of many short non-string arguments (DeepSeek-V4: a 16-token parameter
     block for `"a":0,`, so 3), when the markup grows with the arguments;
   - `PromptTemplate`: at least the largest `prompt_tokens / 2 - request_bytes`,
     which the shortest requests set;
   - `InputSpread`: at least the largest difference between one build's
     `rendered_input` and another build's `prompt_tokens` for the same request;
   - `ThinkingKwarg`: the chat template option of the reasoning mode, when the
     builds default to different modes.
4. `TestRecordedHonestClaimsStayWithinTheByteBounds` (gateway) and
   `TestRecordedHonestAnswersPassTheVotersCounts` (voters) must pass on every
   recording, and `TestRecordedForgeriesAreVotedInflated` must still catch the
   forgeries at the new bounds.

A model outside the table is held to `max_tokens` alone and its rendered checks
are left out.

## Metrics

| metric | labels | meaning |
|---|---|---|
| `devshard_challenge_votes_total` | `fault` | kept votes by fault, and votes not kept (`unchecked`, `late`, `no_render`, `render_busy`) |
| `devshard_challenge_vote_checks_total` | `check` | kept faults by the check that found them (`hash`, `positions`, `rendered_output`, `rendered_input`) |
| `devshard_challenge_render_seconds` | `count`, `model`, `outcome` | time of each render count (`output`, `input`; a table model or `other`; `ok`, `refused`, `busy`, `failed`) |

The gateway's challenge counter carries `reason`, `participant_key` and
`outcome`; `devshard_gateway_challenge_rejects_total` counts the reject causes
by voter `participant_key` and `cause`. A `none` outcome means that no vote
found a fault and the weight of unanswered hosts could not have changed the
result. Its share among `output_above_bytes` and `input_above_bytes` challenges
measures checks that found no fault, not the frequency of honest answers: some
checks can be skipped and other kinds of inflation can remain undetected.

The `challenge vote kept` log line of a counted vote carries the model, the
claims, the positions, both rendered counts and limits, where each count was
made (`output_render`, `input_render`: `ml_node` or the server's address), the
checks left out and why, `ratio_out` (claim over the rendered output) and the
devshardd version.
