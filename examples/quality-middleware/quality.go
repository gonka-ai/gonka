package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"sort"
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

// meshVec stores an int8-quantized embedding alongside its float32 L2 norm.
// Quantization: float32 → int8 at load time; search uses integer dot products
// for CPU throughput. Norm is stored as float32 for the final cosine step.
type meshVec struct {
	q    []int8   // quantized to [-127,127]
	norm float32  // ||original float32 vec||
}

// quantize converts a float32 embedding to int8 and returns its norm.
// Scale: max(|x|) → 127. If all zeros, returns nil.
func quantize(v []float32) (meshVec, bool) {
	if len(v) == 0 {
		return meshVec{}, false
	}
	var maxAbs float32
	for _, x := range v {
		if a := float32(math.Abs(float64(x))); a > maxAbs {
			maxAbs = a
		}
	}
	if maxAbs == 0 {
		return meshVec{}, false
	}
	scale := 127.0 / maxAbs
	q := make([]int8, len(v))
	var norm float64
	for i, x := range v {
		q[i] = int8(math.Round(float64(x) * float64(scale)))
		norm += float64(x) * float64(x)
	}
	return meshVec{q: q, norm: float32(math.Sqrt(norm))}, true
}

// cosineBps computes cosine similarity in basis points [0, 10000]
// between a quantized vector and a raw float32 query.
// Uses the same int8 dot product as DotInt8Bps in patternslot — no GPU needed.
func cosineBps(stored meshVec, query []float32) uint32 {
	if len(stored.q) != len(query) || stored.norm == 0 {
		return 0
	}
	var dot float64
	var qnorm float64
	for i, x := range query {
		dot += float64(stored.q[i]) * float64(x)
		qnorm += float64(x) * float64(x)
	}
	if qnorm == 0 {
		return 0
	}
	// stored.norm was computed from the original float32 values;
	// stored.q is scaled by 127/maxAbs — adjust dot accordingly.
	// We recompute stored norm in float32 domain from q for correctness.
	var sq float64
	for _, b := range stored.q {
		sq += float64(b) * float64(b)
	}
	if sq == 0 {
		return 0
	}
	cos := dot / (math.Sqrt(sq) * math.Sqrt(qnorm))
	if cos > 1 {
		cos = 1
	}
	if cos < 0 {
		cos = 0
	}
	return uint32(cos * 10000)
}

// MeshPoolEntry records a semantic signal from any node/client in the CPU pool.
// Vec is the float32 embedding from the embedder; quantized form is stored in vec.
type MeshPoolEntry struct {
	NodeID     string    `json:"node_id"`
	PromptHash string    `json:"prompt_hash"`
	SlotID     string    `json:"slot_id,omitempty"`
	SimBps     uint32    `json:"sim_bps"`
	HitMode    string    `json:"hit_mode"` // "l1", "l2", "slot", "miss"
	LatencyMs  float64   `json:"latency_ms"`
	Quality    float32   `json:"quality"`   // 0–1; decays on misses, grows on hits
	UseCount   int64     `json:"use_count"`
	Timestamp  time.Time `json:"timestamp"`
	// Vec is the raw embedding; not serialized in stats responses.
	Vec        []float32 `json:"-"`
}

// MeshSearchResult is returned by MeshSearch.
type MeshSearchResult struct {
	Entry   *MeshPoolEntry
	SimBps  uint32
}

// MeshPoolStats aggregates cross-node semantic signals.
type MeshPoolStats struct {
	TotalSignals int64            `json:"total_signals"`
	NodeCount    int              `json:"node_count"`
	AvgSimBps    float64          `json:"avg_sim_bps"`
	SlotHits     int64            `json:"slot_hits"`
	L2Hits       int64            `json:"l2_hits"`
	BestSlotID   string           `json:"best_slot_id,omitempty"`
	BestSlotSim  uint32           `json:"best_slot_sim_bps,omitempty"`
	NodeSignals  map[string]int64 `json:"node_signals"`
	// Search quality: fraction of signals with quality ≥ MinPoolQuality
	HighQualityFrac float64 `json:"high_quality_frac"`
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

const (
	// MinPoolQuality is the minimum quality score for a pool entry to be
	// considered in similarity search. Entries below this are garbage-collected.
	MinPoolQuality float32 = 0.40
	// MinSearchSimBps is the minimum cosine similarity (in basis points) for
	// a pool entry to be returned from MeshSearch. Below this = noise.
	MinSearchSimBps uint32 = 7500
	// MaxPoolEntries limits memory. Eviction removes lowest quality*recency.
	MaxPoolEntries = 4096
)

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

	// Mesh CPU pool: indexed for cosine search, quality-gated
	meshMu   sync.Mutex
	pool     []*meshPoolItem        // ordered by insertion
	poolIdx  map[string]*meshPoolItem // promptHash → item (dedup)
	meshNodes map[string]int64       // nodeID → signal count

	threshold float64 // SimilarityThresholdBps / 10000
}

// meshPoolItem is the internal pool record with quantized embedding.
type meshPoolItem struct {
	entry MeshPoolEntry
	vec   meshVec // quantized for fast CPU cosine search
}

func New(similarityThresholdBps int) *QualityMiddleware {
	return &QualityMiddleware{
		threshold: float64(similarityThresholdBps) / 10000.0,
		poolIdx:   make(map[string]*meshPoolItem),
		meshNodes: make(map[string]int64),
	}
}

// RecordMeshSignal ingests a semantic signal into the CPU pool.
// If Vec is non-nil, the entry is indexed for cosine search.
// If an entry with the same PromptHash already exists, quality and use-count
// are updated rather than creating a duplicate (dedup).
func (qm *QualityMiddleware) RecordMeshSignal(entry MeshPoolEntry) {
	qm.meshMu.Lock()
	defer qm.meshMu.Unlock()
	entry.Timestamp = time.Now()

	key := entry.PromptHash
	if key == "" {
		key = entry.NodeID + "|" + entry.SlotID
	}

	if existing, ok := qm.poolIdx[key]; ok {
		// Update quality: hits raise it, misses lower it
		switch entry.HitMode {
		case "slot", "l2", "l1":
			existing.entry.Quality = min32(1.0, existing.entry.Quality+0.05)
			existing.entry.UseCount++
		case "miss":
			existing.entry.Quality = max32(0, existing.entry.Quality-0.10)
		}
		existing.entry.SimBps = entry.SimBps
		existing.entry.LatencyMs = entry.LatencyMs
		qm.meshNodes[entry.NodeID]++
		return
	}

	item := &meshPoolItem{entry: entry}
	if len(entry.Vec) > 0 {
		if qv, ok := quantize(entry.Vec); ok {
			item.vec = qv
		}
	}
	if entry.Quality == 0 {
		item.entry.Quality = 0.60 // initial quality for new entries
	}
	qm.pool = append(qm.pool, item)
	qm.poolIdx[key] = item
	qm.meshNodes[entry.NodeID]++
	qm.meshEvict()
}

// MeshSearch finds the top-k pool entries most similar to query.
// Only entries with quality ≥ MinPoolQuality and simBps ≥ MinSearchSimBps
// are returned — aggressive noise rejection on pure CPU.
// query must be the same dimensionality as stored embeddings.
func (qm *QualityMiddleware) MeshSearch(query []float32, topK int) []MeshSearchResult {
	qm.meshMu.Lock()
	defer qm.meshMu.Unlock()

	type candidate struct {
		item   *meshPoolItem
		simBps uint32
	}
	var results []candidate

	for _, item := range qm.pool {
		if item.entry.Quality < MinPoolQuality {
			continue
		}
		if len(item.vec.q) == 0 {
			// No embedding stored — can only use for count stats, not search
			continue
		}
		sim := cosineBps(item.vec, query)
		if sim < MinSearchSimBps {
			continue
		}
		results = append(results, candidate{item, sim})
	}

	// Sort descending by similarity
	sort.Slice(results, func(i, j int) bool {
		return results[i].simBps > results[j].simBps
	})
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	out := make([]MeshSearchResult, len(results))
	for i, c := range results {
		out[i] = MeshSearchResult{Entry: &c.item.entry, SimBps: c.simBps}
	}
	return out
}

// meshEvict removes the lowest quality*recency entries when pool exceeds MaxPoolEntries.
// Must be called with meshMu held.
func (qm *QualityMiddleware) meshEvict() {
	if len(qm.pool) <= MaxPoolEntries {
		return
	}
	// Score = quality × exp(-age_days/30)
	now := time.Now()
	type scored struct {
		idx   int
		score float64
	}
	scores := make([]scored, len(qm.pool))
	for i, item := range qm.pool {
		days := now.Sub(item.entry.Timestamp).Hours() / 24
		scores[i] = scored{i, float64(item.entry.Quality) * math.Exp(-days/30)}
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].score > scores[j].score })

	keep := MaxPoolEntries * 3 / 4
	kept := make([]*meshPoolItem, keep)
	newIdx := make(map[string]*meshPoolItem, keep)
	for i := 0; i < keep; i++ {
		item := qm.pool[scores[i].idx]
		kept[i] = item
		key := item.entry.PromptHash
		if key == "" {
			key = item.entry.NodeID + "|" + item.entry.SlotID
		}
		newIdx[key] = item
	}
	qm.pool = kept
	qm.poolIdx = newIdx
}

func (qm *QualityMiddleware) meshPoolStats() *MeshPoolStats {
	qm.meshMu.Lock()
	defer qm.meshMu.Unlock()
	if len(qm.pool) == 0 {
		return nil
	}

	stats := &MeshPoolStats{
		TotalSignals: int64(len(qm.pool)),
		NodeCount:    len(qm.meshNodes),
		NodeSignals:  make(map[string]int64),
	}

	var simSum float64
	var bestSim uint32
	var bestSlot string
	var highQ int64
	for _, item := range qm.pool {
		e := &item.entry
		simSum += float64(e.SimBps)
		if e.HitMode == "slot" {
			stats.SlotHits++
		} else if e.HitMode == "l2" || e.HitMode == "l1" {
			stats.L2Hits++
		}
		if e.SimBps > bestSim {
			bestSim = e.SimBps
			bestSlot = e.SlotID
		}
		if e.Quality >= MinPoolQuality {
			highQ++
		}
	}
	if len(qm.pool) > 0 {
		stats.AvgSimBps = simSum / float64(len(qm.pool))
		stats.HighQualityFrac = float64(highQ) / float64(len(qm.pool))
	}
	stats.BestSlotID = bestSlot
	stats.BestSlotSim = bestSim
	for k, v := range qm.meshNodes {
		stats.NodeSignals[k] = v
	}
	return stats
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
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
		const maxLatencies = 10000
		if len(qm.latencies) >= maxLatencies {
			copy(qm.latencies, qm.latencies[len(qm.latencies)-maxLatencies/2:])
			qm.latencies = qm.latencies[:maxLatencies/2]
		}
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

// SearchHandler serves POST /quality/search — CPU cosine search over the pool.
// Body: {"query": [0.1, 0.2, ...], "top_k": 5}
// Returns candidates with simBps ≥ MinSearchSimBps, quality-gated.
func (qm *QualityMiddleware) SearchHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Query []float32 `json:"query"`
			TopK  int       `json:"top_k"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Query) == 0 {
			http.Error(w, "query vector required", http.StatusBadRequest)
			return
		}
		if req.TopK <= 0 {
			req.TopK = 5
		}
		results := qm.MeshSearch(req.Query, req.TopK)
		w.Header().Set("Content-Type", "application/json")
		type hit struct {
			NodeID    string  `json:"node_id"`
			SlotID    string  `json:"slot_id,omitempty"`
			SimBps    uint32  `json:"sim_bps"`
			HitMode   string  `json:"hit_mode"`
			Quality   float32 `json:"quality"`
			UseCount  int64   `json:"use_count"`
			LatencyMs float64 `json:"latency_ms"`
		}
		out := make([]hit, len(results))
		for i, r := range results {
			out[i] = hit{
				NodeID:    r.Entry.NodeID,
				SlotID:    r.Entry.SlotID,
				SimBps:    r.SimBps,
				HitMode:   r.Entry.HitMode,
				Quality:   r.Entry.Quality,
				UseCount:  r.Entry.UseCount,
				LatencyMs: r.Entry.LatencyMs,
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"results":       out,
			"count":         len(out),
			"min_sim_bps":   MinSearchSimBps,
			"min_quality":   MinPoolQuality,
		})
	})
}

// ShareHandler serves POST /quality/slots/share — participant pushes slots to the mesh pool.
// This is how participant 1 sends binary patterns to participant 2 through the shared pool.
//
// Body: {"node_id": "...", "slots": [{"slot_id": "...", "vec": [...], "hit_mode": "slot", ...}]}
// Returns: {"accepted": N, "pool_size": M}
func (qm *QualityMiddleware) ShareHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			NodeID string          `json:"node_id"`
			Slots  []MeshPoolEntry `json:"slots"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.NodeID == "" || len(req.Slots) == 0 {
			http.Error(w, "node_id and slots required", http.StatusBadRequest)
			return
		}

		const maxShareBatch = 100
		accepted := 0
		for i, slot := range req.Slots {
			if i >= maxShareBatch {
				break
			}
			if slot.NodeID == "" {
				slot.NodeID = req.NodeID
			}
			if len(slot.Vec) == 0 {
				continue
			}
			if slot.HitMode == "" {
				slot.HitMode = "slot"
			}
			qm.RecordMeshSignal(slot)
			accepted++
		}

		qm.meshMu.Lock()
		poolSize := len(qm.pool)
		qm.meshMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"accepted":  accepted,
			"pool_size": poolSize,
		})
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
