package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type AxisScore struct {
	L8Latency    float64 `json:"l8_latency_ms"`
	L9Completion bool    `json:"l9_completion"`
	DX4Tokens    int     `json:"dx4_tokens_used"`
	PromptHash   string  `json:"prompt_hash"`
	CacheLevel   int     `json:"cache_level"`
	CacheHit     bool    `json:"cache_hit"`
}

type EpochSummary struct {
	Epoch              uint64  `json:"epoch"`
	TotalRequests      int64   `json:"total_requests"`
	CacheHits          int64   `json:"cache_hits"`
	CacheMisses        int64   `json:"cache_misses"`
	HitRate            float64 `json:"hit_rate"`
	AvgLatencyMs       float64 `json:"avg_latency_ms"`
	LatencyCV          float64 `json:"latency_cv"`
	CompletionRate     float64 `json:"completion_rate"`
	FeedbackResolved   int64   `json:"feedback_resolved"`
	FeedbackUnresolved int64   `json:"feedback_unresolved"`
}

type QualityMiddleware struct {
	hits       atomic.Int64
	misses     atomic.Int64
	total      atomic.Int64
	completions atomic.Int64
	failures   atomic.Int64
	feedbackOK atomic.Int64
	feedbackNo atomic.Int64

	mu        sync.Mutex
	latencies []float64

	threshold float64 // SimilarityThresholdBps / 10000
}

func New(similarityThresholdBps int) *QualityMiddleware {
	return &QualityMiddleware{
		threshold: float64(similarityThresholdBps) / 10000.0,
	}
}

func CanonicalPromptHash(messages []map[string]string) string {
	canonical, _ := json.Marshal(messages)
	h := sha256.Sum256(canonical)
	return hex.EncodeToString(h[:])
}

func (qm *QualityMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		qm.total.Add(1)

		rec := &responseRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)

		latency := float64(time.Since(start).Milliseconds())
		qm.mu.Lock()
		qm.latencies = append(qm.latencies, latency)
		qm.mu.Unlock()

		if rec.status >= 200 && rec.status < 400 {
			qm.completions.Add(1)
		} else {
			qm.failures.Add(1)
		}

		if rec.Header().Get("X-Cache") == "HIT" {
			qm.hits.Add(1)
		} else {
			qm.misses.Add(1)
		}

		if feedback := r.Header.Get("X-Inference-Feedback"); feedback != "" {
			var fb struct {
				InferenceID string `json:"inference_id"`
				Outcome     string `json:"outcome"`
			}
			if json.Unmarshal([]byte(feedback), &fb) == nil {
				if fb.Outcome == "resolved" {
					qm.feedbackOK.Add(1)
				} else {
					qm.feedbackNo.Add(1)
				}
			}
		}
	})
}

func (qm *QualityMiddleware) Stats() EpochSummary {
	total := qm.total.Load()
	hits := qm.hits.Load()
	misses := qm.misses.Load()
	completions := qm.completions.Load()

	var hitRate, completionRate, avgLat, cv float64
	if total > 0 {
		hitRate = float64(hits) / float64(total)
		completionRate = float64(completions) / float64(total)
	}

	qm.mu.Lock()
	lats := make([]float64, len(qm.latencies))
	copy(lats, qm.latencies)
	qm.mu.Unlock()

	if len(lats) > 0 {
		var sum float64
		for _, l := range lats {
			sum += l
		}
		avgLat = sum / float64(len(lats))

		if avgLat > 0 && len(lats) > 1 {
			var sqDiff float64
			for _, l := range lats {
				d := l - avgLat
				sqDiff += d * d
			}
			variance := sqDiff / float64(len(lats)-1)
			if variance > 0 {
				stddev := math.Sqrt(variance)
				cv = stddev / avgLat
			}
		}
	}

	return EpochSummary{
		TotalRequests:      total,
		CacheHits:          hits,
		CacheMisses:        misses,
		HitRate:            hitRate,
		AvgLatencyMs:       avgLat,
		LatencyCV:          cv,
		CompletionRate:     completionRate,
		FeedbackResolved:   qm.feedbackOK.Load(),
		FeedbackUnresolved: qm.feedbackNo.Load(),
	}
}

func (qm *QualityMiddleware) StatsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(qm.Stats())
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}
