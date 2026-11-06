# Qwen two model dataset

`qwen-two-models-250.jsonl` is a synthetic load-test dataset with 250 rows:

- 125 for `Qwen/Qwen3-4B-Instruct-2507`.
- 125 for `Qwen/Qwen2.5-7B-Instruct`.

Each model receives the same 125 task definitions so their workload content is
comparable. Tasks cover arithmetic, sorting, JSON extraction and computation,
summarization with varying input lengths, and multi-turn conversations. Prompts
are unique within a model; identical prompts across models are intentional.

Half of each model's rows use streaming, rounded up to 63 of 125 rows. The
remaining rows are non-streaming. Temperatures are 0 and 0.7; `max_tokens`
uses 32, 64, 128, and 256. Each cap is also at least the fixture response
length in characters because Mock ML validation uses one position per character. Logprobs are not requested because the
provided remote model listings do not advertise them.

The combined message content is below 2048 UTF-8 bytes per row, leaving a
conservative margin for output and chat-template overhead under the advertised
4096-token context. This byte bound is not an exact tokenizer measurement.
`max_tokens` is an output cap, not a promise about actual response length.

## Response fixtures

The `response` values are authored fixtures for the local Mock ML replay
format. They are not captured outputs from either Qwen model and must not be
used to claim model quality or response equivalence. Real inference may answer
differently. This dataset does not contain credentials or copied production
conversations.

## Local smoke run

From the repository root:

```bash
cd devshard/testenv

go run ./cmd/loadtest \
  -scenario loadtest/scenarios/qwen-two-models-smoke.yaml \
  -load-dataset loadtest/resources/qwen-two-models-250.jsonl
```

The companion scenario runs a local Docker stack with Mock ML for 30 seconds,
two client requests per second, at most two in flight, and no injected faults or escrow
rotation. It does not connect to the remote Qwen deployment. Model traffic
weights are 50/50 and can be changed in YAML independently of dataset row
counts. Both configured models must have dataset rows.

## Remote run

To send these fixture requests to an existing remote gateway without Docker:

```bash
make -C devshard/testenv loadtest-remote \
  SCENARIO=qwen-two-models-smoke \
  LOAD_DATASET=loadtest/resources/qwen-two-models-250.jsonl
```

Run the command from the repository root. The Make target reads GONKA_BASE and
GONKA_API_KEY from devshard/testenv/.env.remote-load. For this multi-model
scenario, GONKA_MODEL is ignored: model IDs and weights come from YAML.
Each selected dataset row supplies its request parameters and messages.
Fixture responses are not sent or compared against real model output.

The remote runner reports client error and drop rates and latency, saving
run.yaml, summary.json, requests.jsonl, assertions.json, and report.md in a new
loadtest/results/remote-* directory. It does not provision or modify escrows
and does not evaluate the local scenario's host or server state assertions.
Host faults and events are rejected. For a single-model YAML scenario,
GONKA_MODEL (or an explicit -model flag on cmd/remote-loadtest) can override the
model; the dataset must contain rows for that target model.
