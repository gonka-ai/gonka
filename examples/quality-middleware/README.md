# gonka-quality-middleware

Go middleware for measuring inference quality axes on requests passing through [gonkalabs/opengnk](https://github.com/gonkalabs/opengnk).

Implements measurement layer for [GiP #860 — Inference Quality Axis Registry](https://github.com/gonka-ai/gonka/discussions/860).

## What it measures

| Axis | Field | Source |
|------|-------|--------|
| L6 Cache Reuse | `cache_hits`, `hit_rate` | `X-Cache: HIT` response header |
| L8 Latency stability | `latency_cv` | CV = stddev/mean per epoch |
| L9 Completion rate | `completion_rate` | HTTP 2xx/4xx status |
| DX Feedback | `feedback_resolved` | `X-Inference-Feedback` request header |

## Usage

```go
import quality "github.com/Mayveskii/gonka-quality-middleware"

qm := quality.New(9700) // SimilarityThresholdBps = 0.97

mux := http.NewServeMux()
mux.Handle("/quality/stats", qm.StatsHandler())

http.ListenAndServe(":8080", qm.Wrap(mux))
```

## CanonicalPromptHash

SHA-256 of canonical JSON messages array — for L1 exact-match cache key.

```go
hash := quality.CanonicalPromptHash([]map[string]string{
    {"role": "user", "content": "what is gonka?"},
})
```

## Stats endpoint

`GET /quality/stats` returns `EpochSummary`:

```json
{
  "total_requests": 150,
  "cache_hits": 45,
  "cache_misses": 105,
  "hit_rate": 0.3,
  "avg_latency_ms": 420.5,
  "latency_cv": 0.68,
  "completion_rate": 0.9433,
  "feedback_resolved": 12,
  "feedback_unresolved": 3
}
```

## Tests

```
go test ./...
```

7/7 PASS, no external dependencies, no GPU required.

## Integration target

Designed as a drop-in middleware layer for [gonkalabs/opengnk](https://github.com/gonkalabs/opengnk). Each request through the proxy is measured; stats accumulate per epoch and are exposed via `/quality/stats`.

Feeds data to [PR #859](https://github.com/gonka-ai/gonka/pull/859) `QualityReporter` via the `X-Cache` header contract.
