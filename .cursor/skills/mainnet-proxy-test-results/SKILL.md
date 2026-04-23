---
name: mainnet-proxy-test-results
description: Pull and analyze Gonka mainnet subnet proxy test results from subnetctl and nginx logs. Use when the user asks to grab latest results, reset the log cursor, compare client failures with server logs, inspect request IDs, classify bad hosts, or analyze mainnet proxy test runs.
---

# Mainnet Proxy Test Results

## Use This Skill For

- Pulling the next incremental `subnetctl-multi` and nginx log window from mainnet.
- Updating the local cursor used to delimit test-result snapshots.
- Analyzing `mainnet-proxy-tests` logs after a stress run.
- Correlating client failures with:
  - `X-Request-Id`
  - `proxy_response_finished`
  - nginx `rc`, `urb`, `bs`, `conn`
- Classifying bad hosts by failure type.

## Important Safety Rule

This workflow touches the mainnet server. Before any remote command:

1. Show the exact command you plan to run.
2. Ask for approval.
3. Only execute after explicit approval.

When remote access is needed, also follow the project skill `gonka-mainnet-server`.

## Files Used

- Cursor file:
  - `mainnet-proxy-tests/logs/mainnet_subnetctl_log_cursor.txt`
- Subnetctl snapshots:
  - `mainnet-proxy-tests/logs/mainnet-subnetctl-multi-since-<since>-<until>.log`
- Nginx snapshots:
  - `mainnet-proxy-tests/logs/proxy-nginx-<since>-<until>-combined.log`
- Analyzer:
  - `mainnet-proxy-tests/tools/analyze_subnetctl_log.py`

## Standard Snapshot Workflow

### 1. Read the cursor

Read:

- `mainnet-proxy-tests/logs/mainnet_subnetctl_log_cursor.txt`

Extract:

- `NEXT_LOG_SINCE_ISO_UTC`

### 2. Freeze the end timestamp

Capture current UTC time. Use it as the `until` boundary for both pulls so:

- `subnetctl`
- nginx

cover the exact same time window.

### 3. Pull subnetctl logs

Use merged stdout/stderr:

```bash
gcloud compute ssh --zone "us-central1-f" "network-node-4" --project "decentralized-ai" --command "sudo docker logs --since '<SINCE>' --until '<UNTIL>' subnetctl-multi 2>&1" > "/ABS/PATH/mainnet-proxy-tests/logs/mainnet-subnetctl-multi-since-<SINCE_COMPACT>-<UNTIL_COMPACT>.log"
```

### 4. Pull nginx logs

Always use the combined version so access lines and `info` / `warn` notices live together:

```bash
gcloud compute ssh --zone "us-central1-f" "network-node-4" --project "decentralized-ai" --command "sudo docker logs --since '<SINCE>' --until '<UNTIL>' proxy 2>&1" > "/ABS/PATH/mainnet-proxy-tests/logs/proxy-nginx-<SINCE_COMPACT>-<UNTIL_COMPACT>-combined.log"
```

### 5. Verify the files landed

Check:

- file size is non-zero
- first few lines look plausible

### 6. Advance the cursor

Update `mainnet_subnetctl_log_cursor.txt`:

- set `NEXT_LOG_SINCE_ISO_UTC=<UNTIL>`
- note the latest subnetctl filename
- note the latest nginx filename

## Standard Analysis Workflow

### Subnetctl batch analysis

Run:

```bash
python3 mainnet-proxy-tests/tools/analyze_subnetctl_log.py <subnetctl-log-file>
```

Use the analyzer for:

- requests
- attempts
- escalation triggers
- bad-host summary
- delivery / flush outcome

Key delivery signals:

- `proxy_response_finished`
- `proxy_flush_failed`
- `proxy_client_disconnected`
- `proxy_stream_failed`
- `proxy_done_write_failed`

Interpretation:

- `proxy_response_finished outcome=ok flush_failed=false final_flush_err absent`
  means the app believes it completed the stream normally.
- `proxy_flush_failed` or `final_flush_err` means the server noticed client-side delivery trouble directly.

### Nginx batch analysis

Use the combined nginx file to inspect:

- `rc="OK"` vs `rc=""`
- `xid="req-..."`
- `ust`
- `urb`
- `bs`
- `conn=<id>:<request_count>`
- `closed keepalive connection`
- `prematurely closed connection`
- `Connection reset by peer`

Interpretation:

- `rc="OK"` means nginx believes it fully wrote the response to the client socket.
- `rc=""` means nginx did not complete delivery.
- `prematurely closed connection` means nginx observed the client side die first.

## Per-Request Correlation Workflow

When the user provides a failed client record:

1. Extract:
   - `server_request_id` / `x-request-id`
   - `chunks`
   - `http_body_bytes_received`
   - `sse_payload_bytes_received`
   - `connection_reused`
   - `total_ms`
2. Search that request id in:
   - subnetctl snapshot
   - nginx combined snapshot
3. Compare:
   - client bytes received
   - subnetctl `proxy_response_finished bytes_written`
   - nginx `urb`
   - nginx `bs`
   - client chunks vs server `output_chunks`

### Standard conclusions

- If:
  - subnetctl says `proxy_response_finished outcome=ok`
  - nginx says `rc="OK"`
  - client got only a prefix

  then the break is likely downstream of normal server streaming logic.

- If subnetctl shows:
  - `proxy_flush_failed`
  - `proxy_client_disconnected`
  - `final_flush_err`

  then the server observed delivery failure directly.

## Bad Host Classification

For current runs, use these buckets:

- `broken_route_404`
  - `send_failed` with `status 404`
- `empty_stream`
  - `empty_stream`
  - or `attempt_failed` whose correlated lines show:
    - `first_token`
    - `send_completed`
    - `race_completed finished=false responsive=true output_chunks=2 content_chunks=0`
- `receipt_no_output_unfinished`
  - correlated lines show:
    - `receipt_received`
    - `send_completed`
    - no `empty_stream`
    - `race_completed finished=false responsive=true output_chunks=0 content_chunks=0`

When asked “who are the bad hosts,” summarize:

- host
- count
- inferred type
- whether it ever had a good finished response in the same batch

## Quarantine-Related Notes

Recent behavior to remember:

- inference `404` is treated as a short quarantine signal
- 3 consecutive `empty_stream` responses trigger short quarantine
- empty-stream streak resets:
  - on quarantine application
  - on a later good finished response

When analyzing post-deploy logs, verify these hosts stop reappearing repeatedly after the threshold is crossed.

## Reporting Style

When reporting results back to the user:

1. State which files were pulled.
2. Summarize the batch:
   - requests
   - attempts
   - extra attempts
   - delivery/flush outcome
3. List bad hosts by type.
4. Call out whether nginx saw:
   - `rc=""`
   - resets
   - premature closes
5. If a request id was provided, compare client bytes/chunks with server-side bytes/chunks explicitly.

Keep the answer concise, but include the hard numbers.
