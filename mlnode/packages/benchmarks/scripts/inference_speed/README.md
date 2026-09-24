# Inference speed under agent sessions

`session_bench.py` measures the tokens per second per GPU that a running MLNode
delivers to agent-style sessions, and how that changes with prefix-cache reuse.

The load is vLLM's own multi-turn benchmark client (`benchmarks/multi_turn`, fetched
from a pinned vLLM commit on first run), wrapped by `mt_bust.py`. Every session keeps
one request in flight and resends its whole history each turn; all sessions share one
system prompt. The session profile:

| part | tokens |
|---|---|
| shared system prompt | 6 000 |
| per-session task context | lognormal, mean 4 000, max 16 000 |
| turns per session | 10–20 (tool output + answer) |
| tool output per turn | lognormal, mean 800, max 4 000 |
| answer per turn | 100–400 |

The same number of sessions is run three times, once per cache-hit level:

| column | what the engine can take from the prefix cache |
|---|---|
| high hit | the whole resent history |
| low hit | only the shared system prompt: a one-off marker right after it makes the rest unique |
| zero hit | nothing: a one-off marker at the very start of every request |

Each column is time-boxed: sessions start spread over 30 s, the server's Prometheus
counters are sampled twice a second, and once the generation rate has settled a
180 s window is measured. All rates come from the server, so client start-up, ramp
and drain do not enter them. With a 90 s window a run took 9–15 minutes on 1× B300; the default is now 180 s,
since 90 s runs of 256 sessions repeated only within 5–16 %.

## Running it

Inside the MLNode container, with a model already loaded:

```bash
docker cp session_bench.py mt_bust.py mlnode:/root/.cache/
docker exec mlnode bash -c "cd /root/.cache && \
  python3 session_bench.py --sessions 32 --trust-remote-code"
```

`--sessions` is concurrent sessions **per GPU**; the script finds every vLLM instance
on ports 5001–5008 and spreads the sessions over them. The first run needs internet
access to fetch the client files and `pandas`, which the client imports.

Useful options: `--modes high,low,zero` (any subset), `--window` (measured seconds per
column), `--label` (file name of the result), `--out` (result directory).

## Output

A table per run, plus a JSON with every number and the rate trace of the ramp:

| row | meaning |
|---|---|
| output tok/s/GPU | generated tokens |
| prefill computed tok/s/GPU | prompt tokens the engine computed (prompt minus cache hits) |
| billed tok/s/GPU | prompt plus output tokens, cache hits included |
| prefix cache hit | cached prompt tokens / prompt tokens |
| mean input tokens / request | prompt length the window saw; billed tracks it closely |
| TTFT, ITL, tok/s per session | server-side means over the window |
| running / waiting | requests on the GPU / queued for room |
| KV overflow | KV cache ≥ 95 % full, requests waiting, or requests preempted |

Once KV overflows, sessions evict each other's history: the hit rate falls below
what the workload allows and the high-hit column stops being one. Lower `--sessions`
until the flag clears to find the load the deployment actually sustains.
