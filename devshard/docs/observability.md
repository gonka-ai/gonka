# Devshard observability contract

Devshard observability exists to answer one operational question quickly:

```
Where did this inference stop?
```

Every `/sessions/:id/chat/completions` request must produce one explicit terminal
outcome. Operators must not infer missing receipt, missing execution, or missing
`MsgFinishInference` from absent logs.

## Acceptance questions

The implementation must answer these questions from metrics first and logs second:

- Which requests ended without a receipt?
- Which requests returned a receipt but did not start execution?
- Which requests started execution but did not publish `MsgFinishInference`?
- Which interruptions were expected protocol outcomes, and which are
  operator-actionable failures?

In this document, `MsgFinishInference` means the devshard protobuf transaction
added to the per-host devshard mempool by `Host.RunExecution`.

## Operator workflow

Use metrics to find the class of problem:

```promql
rate(devshard_request_terminal_total[5m])
rate(devshard_interruption_total[5m])
rate(devshard_receipt_orphan_total[5m])
rate(devshard_validation_orphan_total[5m])
```

Then use logs to find the exact request:

1. Search for `request_id`.
2. Read the `devshard request terminal` line.
3. Inspect `terminal`, `reason`, and `failure_where`.
4. Search the same `request_id` for WARN or ERROR to see the failure-site log
   with `where`, `reason`, and `error`.

Metrics may aggregate. Logs must keep the exact bounded `reason`.

## Log contract

Every devshard lifecycle log line carries:

- `request_id` when available.
- `escrow_id` when known.
- `stage`.
- `where`.
- `binary`.
- `version`.
- `mode`.

WARN and ERROR lines also carry:

- `reason`.
- `error` when an error exists.
- `sender` when known and relevant.

Terminal lines also carry:

- `terminal`.
- `reason`.
- `failure_where` when the terminal was caused by a concrete stop point.
- `inference_id` and `nonce` when known.

`where` is a stable symbolic code location, not a file:line reference. It must
stay queryable across releases. The message text can change; `where` and
`reason` should not change casually.

`reason` is exact but bounded. It should say what failed, not just the subsystem
family. For example, use `missing_auth_headers`, `signature_verify_err`,
`acquire_err`, or `sign_finish_err` instead of a generic `error` when the
failure is known. ML-node metrics use `path=execute|validate`; the same bounded
reason set is used for both paths.

Keep log messages short. Human text is for readability; `where` and `reason` are
the durable query fields.

`mode` is `dapi_inprocess` for devshard served inside dapi and
`standalone_devshardd` for the standalone devshard binary.

## Terminal outcomes

Each chat request increments exactly one
`devshard_request_terminal_total{terminal,reason}` counter and emits exactly one
terminal log line.

| Terminal | Meaning | Operator action |
| --- | --- | --- |
| `finish_published` | `MsgFinishInference` was added to the devshard mempool. | Healthy. |
| `no_receipt_expected` | No receipt was expected, such as no payload, missing target diff, or this host is not executor. | Debug only unless rate changes sharply. |
| `no_receipt_interrupted` | A receipt should have been produced, but the request failed before receipt or failed to write it. | Alert. |
| `receipt_no_execution_expected` | Receipt was returned but no new execution should start, such as `already_executing` or `cached_response`. | Debug only. |
| `receipt_no_execution_interrupted` | Receipt was returned but execution did not start because the request was interrupted before `RunExecution`. | Alert. |
| `execution_no_finish` | Execution started but `MsgFinishInference` was not added to the mempool. | Alert. |
| `client_cancelled_after_receipt` | Client disconnected after receipt and cancelled execution. | Alert until execution is detached from client context. |

A `finish_published` terminal with `reason=partial_response_after_interruption`
means the host published `MsgFinishInference` from a response reconstructed after
the ML stream ended unexpectedly. Inspect `failure_where` and the matching WARN
line before treating it as healthy.

Alert rules should filter to `no_receipt_interrupted`,
`receipt_no_execution_interrupted`, `execution_no_finish`, and
`client_cancelled_after_receipt`. Exclude `reason="initializing"` from
`no_receipt_interrupted` alerts during startup and readiness transitions.

## Where values

These values are the primary map for answering "where did it stop?"

| where | Covers |
| --- | --- |
| `routes.session_resolve` | Lazy session readiness, version conflicts, epoch conflicts, bridge/state/storage failures before a `transport.Server` exists. |
| `transport.auth` | Missing or invalid auth headers, body read, signature verification, sender authorization. |
| `transport.rate_limit` | Per-sender rate limiting. |
| `transport.handle_inference` | Owner check, JSON parse, request decode, top-level `HandleRequest` error. |
| `host.apply_diff` | Diff apply and persistence before receipt signing. |
| `host.sign_receipt` | Receipt expected/observed decision, payload verification, receipt marshal/sign. |
| `host.sign_state` | State root/signature and `state_signature_withheld`. |
| `transport.write_receipt_sse` | First SSE write of `devshard_receipt`. |
| `runtime.modify_request` | Prompt rewrite before ML execution or validation. |
| `engine.mlnode_call` | Outbound ML node acquisition, HTTP call, HTTP status classification, timeout, and release. |
| `runtime.fetch_payloads` | Validation payload fetch from executor. |
| `runtime.process_execution_response` | SSE/JSON response processing, usage extraction, response hash. |
| `runtime.write_client_response` | Streaming/proxy writes after receipt. |
| `runtime.store_payloads` | Canonical prompt and response payload storage. |
| `host.publish_finish` | Finish message signing and mempool add. |
| `host.validation_queue` | Validation enqueue/drop. |
| `host.validate` | Validation execution and status re-read. |
| `host.publish_validation` | Validation/vote signing and mempool add. |
| `manager.payloads` | Executor payload-serving auth, retrieval, fallback, signing, write. |
| `request.terminal` | Final request outcome summary. |

## Reason values

Reasons should remain low-cardinality and useful to operators. Add a new reason
only when it changes the action an operator takes or identifies a distinct stop
point.

Do not add request ids, escrow ids, model names, node URLs, or free-form errors
to metric labels. Put those in logs.

Request and receipt failures:

```text
missing_auth_headers
invalid_signature_hex
invalid_timestamp
body_read_err
signature_verify_err
sender_not_allowed
rate_limited
missing_sender
owner_err
parse_err
decode_err
handle_request_err
apply_err
persist_diff_err
payload_verify_err
receipt_marshal_err
receipt_sign_err
state_sign_err
receipt_write_err
cached_replay_err
meta_write_err
payload_absent
target_diff_absent
not_executor
already_executing
cached_response
state_signature_withheld
```

ML-node call failures are shared by execution and validation:

```text
acquire_err
transport_err
timeout
http_5xx
http_4xx
application_err
release_err
```

Execution processing failures:

```text
modify_request_err
response_process_err
response_write_err
partial_response_after_interruption
usage_parse_err
canonicalize_prompt_err
payload_store_err
execute_err
sign_finish_err
client_cancelled_after_receipt
```

Session-resolution failures:

```text
initializing
version_conflict
epoch_conflict
build_group_err
get_escrow_err
storage_err
session_resolve_err
```

Validation failures before a verdict:

```text
payload_fetch_err
payload_not_found
payload_auth_err
validation_body_err
original_response_parse_err
enforced_tokens_err
validation_response_err
inference_disappeared
sign_validation_err
sign_vote_err
validation_status_changed
queue_full
```

Payload-serving failures:

```text
missing_inference_id
missing_validator_header
missing_timestamp_header
missing_epoch_header
missing_signature_header
invalid_timestamp
invalid_epoch
timestamp_too_old
timestamp_in_future
not_group_member
pubkey_resolution_err
invalid_signature
payload_not_found
payload_retrieve_err
payload_response_sign_err
payload_write_err
```

Metric labels named `status` use small state labels such as `ok`, `error`,
`queued`, or `cached`. These labels are for aggregate counters only. Logs use
`reason` for the durable failure code.

`host.execute` is a fallback code location for unclassified execution errors. A
specific runtime or engine `where` is preferred for any known failure.

## Validation investigation

Validation publish logs include:

- `validation_flow`: `should_validate` for sampled finished inferences, or
  `challenged` for mandatory challenged-inference votes.
- `validation_tx`: `validation` or `validation_vote`.
- `validation_result`: `valid` or `invalid`.
- `result_valid`: boolean form of the same result for structured queries.
- `validation_reason`: short tag for why the vote was cast. Every emitted
  validation carries one. See below.
- Outcome-specific kv pairs: the values behind the decision (similarity,
  threshold, token counts, mismatch index). Present only when relevant to the
  reason.

### Validation reasons

A vote without a reason is opaque. Each terminal validation outcome maps to one
`validation_reason` tag and a fixed set of detail fields appended to the
`validation tx published` log line.

| validation_reason | result | Detail fields |
| --- | --- | --- |
| `similarity_pass` | valid | `similarity`, `threshold` |
| `similarity_below` | invalid | `similarity`, `threshold` |
| `inflated_tokens` | invalid | `claimed_input`, `validation_input`, `claimed_output`, `validation_output` |
| `different_length` | invalid | `original_logits_len`, `validation_logits_len` |
| `different_tokens` | invalid | `first_mismatch_index`, `original_token`, `validation_token` |
| `invalid_inference` | invalid | `detail`, `error` (when present) |
| `rejected_payload` | valid | `mlnode_status` (400 or 422) |

`rejected_payload` votes valid because the mlnode rejected the prompt as
unservable. The other invalid reasons reflect concrete divergences from the
executor's claim. Errors that prevent reaching a verdict (`payload_fetch_err`,
`http_5xx`, `validation_response_err`, etc.) are emitted on the `validate
failed` line, not the publish line. See the existing validation reasons above.

## Metric set

Devshard request lifecycle:

```text
devshard_inflight{stage}
devshard_request_terminal_total{terminal,reason}
devshard_interruption_total{class,reason}
devshard_session_resolution_total{route,status,reason}
devshard_receipt_orphan_total{reason}
```

Validation and payload serving:

```text
devshard_validation_total{stage,status}
devshard_validation_orphan_total{reason}
devshard_validation_queue_drops_total
devshard_payload_request_total{status,reason}
```

ML node and server health:

```text
devshard_mlnode_attempts_total{path,outcome,node_id}
devshard_mlnode_call_seconds{path,node_id,phase}
devshard_mlnode_tokens{path,node_id,kind}
devshard_http_connections{server,state}
devshard_http_connections_total{server,state}
devshard_validation_queue_depth{escrow_id}
devshard_mempool_size{escrow_id}
devshard_build_info{binary,version,commit}
```

`devshard_request_terminal_total` is the aggregate request lifecycle counter.
Use `devshard_inflight` only for current pressure, not terminal outcomes.

Labels stay low-cardinality. `request_id`, `inference_id`, `sender`,
`escrow_id`, and `mlnode_url` are log-only for aggregate counters. The
`devshard_validation_queue_depth` and `devshard_mempool_size` gauges keep
`escrow_id` because they report local per-session state, bounded by the sessions
served by the process.

## Request ID propagation

Inbound devshard requests read `X-Request-Id` when present and generate one when
missing. The response echoes the selected id.

Outbound devshard calls propagate the same header:

- ML-node execution.
- ML-node validation.
- Validation payload fetches to executor devshards.

Validation jobs synthesize `request_id = "validate-<inference_id>"` because they
are not driven by an inbound user HTTP request.

## Metrics surface

Both runtime modes expose plain `/metrics`:

- dapi in-process devshard metrics are exposed on the existing ML server at
  `api:9100/metrics`.
- standalone `devshardd` metrics are exposed at `/metrics` and are reachable
  through versiond as `versiond:8080/{version}/metrics`.

Prometheus discovery for standalone `devshardd` uses
`api:9100/sd/devshardd`.

## Out of scope

- Distributed tracing.
- New logging infrastructure.
- Source changes in mlnode or vLLM.
- Per-model latency breakdowns.
- Alert-rule files.
