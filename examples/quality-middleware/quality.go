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

// MeshPoolEntry records a semantic signal from any node/client in the CPU pool.
// These are shared across nodes via the binary singularity slot store.
type MeshPoolEntry struct {
	NodeID     string    `json:"node_id"`
	PromptHash string    `json:"prompt_hash"`
	SlotID     string    `json:"slot_id,omitempty"`
	SimBps     uint32    `json:"sim_bps"`
	HitMode    string    `json:"hit_mode"` // "l1", "l2", "slot"
	LatencyMs  float64   `json:"latency_ms"`
	Timestamp  time.Time `json:"timestamp"`
}

// MeshPoolStats aggregates cross-node semantic signals.
type MeshPoolStats struct {
	TotalSignals  int64              `json:"total_signals"`
	NodeCount     int                `json:"node_count"`
	AvgSimBps     float64            `json:"avg_sim_bps"`
	SlotHits      int64              `json:"slot_hits"`
	L2Hits        int64              `json:"l2_hits"`
	BestSlotID    string             `json:"best_slot_id,omitempty"`
	BestSlotSim   uint32             `json:"best_slot_sim_bps,omitempty"`
	NodeSignals   map[string]int64   `json:"node_signals"`
}

type EpochSummary struct {
	Epoch              uint64        `json:"epoch"`
	TotalRequests      int64         `json:"total_requests"`
	CacheHits          int64         `json:"cache_hits"`
	CacheMisses        int64         `json:"cache_misses"`
	HitRate            float64       `json:"hit_rate"`
	AvgLatencyMs       float64       `json:"avg_latency_ms"`
	LatencyCV          float64       `json:"latency_cv"`
	CompletionRate     float64       `json:"completion_rate"`
	FeedbackResolved   int64         `json:"feedback_resolved"`
	FeedbackUnresolved int64         `json:"feedback_unresolved"`
	// Mesh CPU pool metrics (populated when X-BS-Node-ID header is present)
	MeshPool           *MeshPoolStats `json:"mesh_pool,omitempty"`
}

type QualityMiddleware struct {
	hits        atomic.Int64
	misses      atomic.Int64
	total       atomic.Int64
	completions atomic.Int64
	failures    atomic.Int64
	feedbackOK  atomic.Int64
	feedbackNo  atomic.Int64

	mu        sync.Mutex
	latencies []float64

	// Mesh CPU pool: collects signals from all nodes/clients
	meshMu      sync.Mutex
	meshEntries []MeshPoolEntry
	meshNodes   map[string]int64 // nodeID → signal count

	threshold float64 // SimilarityThresholdBps / 10000
}

func New(similarityThresholdBps int) *QualityMiddleware {
	return &QualityMiddleware{
		threshold: float64(similarityThresholdBps) / 10000.0,
		meshNodes: make(map[string]int64),
	}
}

// RecordMeshSignal ingests a semantic signal from a node/client into the pool.
// Called automatically from Wrap() when X-BS-Node-ID header is present,
// or directly by the scenario runner for slot-mode hits.
func (qm *QualityMiddleware) RecordMeshSignal(entry MeshPoolEntry) {
	qm.meshMu.Lock()
	defer qm.meshMu.Unlock()
	entry.Timestamp = time.Now()
	qm.meshEntries = append(qm.meshEntries, entry)
	qm.meshNodes[entry.NodeID]++
}

func (qm *QualityMiddleware) meshPoolStats() *MeshPoolStats {
	qm.meshMu.Lock()
	defer qm.meshMu.Unlock()
	if len(qm.meshEntries) == 0 {
		return nil
	}

	stats := &MeshPoolStats{
		TotalSignals: int64(len(qm.meshEntries)),
		NodeCount:    len(qm.meshNodes),
		NodeSignals:  make(map[string]int64),
	}

	var simSum float64
	var bestSim uint32
	var bestSlot string
	for _, e := range qm.meshEntries {
		simSum += float64(e.SimBps)
		if e.HitMode == "slot" {
			stats.SlotHits++
		} else if e.HitMode == "l2" {
			stats.L2Hits++
		}
		if e.SimBps > bestSim {
			bestSim = e.SimBps
			bestSlot = e.SlotID
		}
	}
	if len(qm.meshEntries) > 0 {
		stats.AvgSimBps = simSum / float64(len(qm.meshEntries))
	}
	stats.BestSlotID = bestSlot
	stats.BestSlotSim = bestSim
	for k, v := range qm.meshNodes {
		stats.NodeSignals[k] = v
	}
	return stats
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

		cacheHeader := rec.Header().Get("X-Cache")
		if cacheHeader == "HIT" {
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

		// Mesh CPU pool signal: capture node identity + slot hit from headers
		// Nodes/clients set X-BS-Node-ID and X-BS-Slot-ID (optional).
		if nodeID := r.Header.Get("X-BS-Node-ID"); nodeID != "" {
			hitMode := "miss"
			if cacheHeader == "HIT" {
				hitMode = "l2"
			}
			if slotID := rec.Header().Get("X-BS-Slot-ID"); slotID != "" {
				hitMode = "slot"
			}
			qm.RecordMeshSignal(MeshPoolEntry{
				NodeID:    nodeID,
				SlotID:    rec.Header().Get("X-BS-Slot-ID"),
				HitMode:   hitMode,
				LatencyMs: latency,
			})
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
		MeshPool:           qm.meshPoolStats(),
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
