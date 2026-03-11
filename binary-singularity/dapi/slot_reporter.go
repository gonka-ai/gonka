// Package dapi extends the DAPI QualityReporter with Binary Singularity metrics.
//
// This file defines the additional fields and methods needed to track
// PatternSlot contributions in the existing CacheQualityEpochSummary.
//
// Integration: these types are used by the scenario runner and can be
// proposed as a DAPI extension (new fields in QualityReporter) once
// the bookworm experiment proves their value.
package dapi

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// SlotQualityEvent represents a single slot execution result
// reported to the DAPI-level quality tracker.
type SlotQualityEvent struct {
	SlotID       string  `json:"slot_id"`
	DomainID     string  `json:"domain_id"`
	EpochIndex   uint64  `json:"epoch_index"`
	Success      bool    `json:"success"`
	SimBps       uint16  `json:"sim_bps"`
	CoherenceBps uint16  `json:"coherence_bps"`
	RewardDelta  float64 `json:"reward_delta"`
	LatencyMs    int64   `json:"latency_ms"`
	Timestamp    string  `json:"timestamp"`
}

// SlotEpochSummary is the per-epoch aggregation of slot metrics,
// intended to extend CacheQualityEpochSummary.
type SlotEpochSummary struct {
	EpochIndex uint64 `json:"epoch_index"`

	// Counters
	SlotHitCount     int64   `json:"slot_hit_count"`
	SlotMissCount    int64   `json:"slot_miss_count"`
	SlotSuccessCount int64   `json:"slot_success_count"`
	SlotFailureCount int64   `json:"slot_failure_count"`
	NewSlotsCreated  int64   `json:"new_slots_created"`

	// Aggregates
	AvgSimBps       uint16  `json:"avg_sim_bps"`
	AvgCoherenceBps uint16  `json:"avg_coherence_bps"`
	AvgSuccessRate  float64 `json:"avg_success_rate_bps"`
	TotalRewardSum  float64 `json:"total_reward_sum"`

	// Domain breakdown
	DomainContributions map[string]DomainContribution `json:"domain_contributions"`

	// Derived — for PQM extension
	SlotHitRate       float64 `json:"slot_hit_rate"`
	GPUInferencesSaved int64  `json:"gpu_inferences_saved"`
}

// DomainContribution tracks per-domain slot effectiveness.
type DomainContribution struct {
	Hits         int64   `json:"hits"`
	Misses       int64   `json:"misses"`
	Successes    int64   `json:"successes"`
	Failures     int64   `json:"failures"`
	AvgSimBps    uint16  `json:"avg_sim_bps"`
	RewardSum    float64 `json:"reward_sum"`
	SlotsCreated int64   `json:"slots_created"`
}

// SlotReporter accumulates slot quality events and produces epoch summaries.
// Mirrors QualityReporter but for the binary singularity layer.
type SlotReporter struct {
	mu           sync.Mutex
	currentEpoch uint64
	events       []SlotQualityEvent

	// Running accumulators
	hitCount     int64
	missCount    int64
	successCount int64
	failureCount int64
	newSlots     int64
	simSum       int64
	cohSum       int64
	rewardSum    float64
	domains      map[string]*DomainContribution
}

// NewSlotReporter creates a fresh reporter.
func NewSlotReporter() *SlotReporter {
	return &SlotReporter{
		domains: make(map[string]*DomainContribution),
	}
}

// RecordHit records a successful slot match.
func (sr *SlotReporter) RecordHit(event SlotQualityEvent) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	sr.events = append(sr.events, event)
	sr.hitCount++
	sr.simSum += int64(event.SimBps)
	sr.cohSum += int64(event.CoherenceBps)
	sr.rewardSum += event.RewardDelta

	if event.Success {
		sr.successCount++
	} else {
		sr.failureCount++
	}

	dc := sr.getDomain(event.DomainID)
	dc.Hits++
	if event.Success {
		dc.Successes++
	} else {
		dc.Failures++
	}
	dc.RewardSum += event.RewardDelta
}

// RecordMiss records a slot miss (no matching slot found).
func (sr *SlotReporter) RecordMiss(domainID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.missCount++
	dc := sr.getDomain(domainID)
	dc.Misses++
}

// RecordNewSlot records creation of a new distilled slot.
func (sr *SlotReporter) RecordNewSlot(domainID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.newSlots++
	dc := sr.getDomain(domainID)
	dc.SlotsCreated++
}

// EpochSummary produces the aggregated summary for the current epoch.
func (sr *SlotReporter) EpochSummary(epochIndex uint64) SlotEpochSummary {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	total := sr.hitCount + sr.missCount
	summary := SlotEpochSummary{
		EpochIndex:       epochIndex,
		SlotHitCount:     sr.hitCount,
		SlotMissCount:    sr.missCount,
		SlotSuccessCount: sr.successCount,
		SlotFailureCount: sr.failureCount,
		NewSlotsCreated:  sr.newSlots,
		TotalRewardSum:   sr.rewardSum,
		GPUInferencesSaved: sr.hitCount, // each slot hit = 1 GPU inference saved
	}

	if sr.hitCount > 0 {
		summary.AvgSimBps = uint16(sr.simSum / sr.hitCount)
		summary.AvgCoherenceBps = uint16(sr.cohSum / sr.hitCount)
		summary.AvgSuccessRate = float64(sr.successCount) / float64(sr.hitCount)
	}

	if total > 0 {
		summary.SlotHitRate = float64(sr.hitCount) / float64(total)
	}

	// Copy domain map
	summary.DomainContributions = make(map[string]DomainContribution)
	for k, v := range sr.domains {
		dc := *v
		if dc.Hits > 0 {
			dc.AvgSimBps = uint16(int64(dc.Hits) * int64(summary.AvgSimBps) / dc.Hits)
		}
		summary.DomainContributions[k] = dc
	}

	return summary
}

// Reset clears all accumulators for the next epoch.
func (sr *SlotReporter) Reset() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.hitCount = 0
	sr.missCount = 0
	sr.successCount = 0
	sr.failureCount = 0
	sr.newSlots = 0
	sr.simSum = 0
	sr.cohSum = 0
	sr.rewardSum = 0
	sr.domains = make(map[string]*DomainContribution)
	sr.events = nil
}

// SaveEvents writes raw events to a JSON file for audit.
func (sr *SlotReporter) SaveEvents(path string) error {
	sr.mu.Lock()
	events := make([]SlotQualityEvent, len(sr.events))
	copy(events, sr.events)
	sr.mu.Unlock()

	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (sr *SlotReporter) getDomain(domainID string) *DomainContribution {
	dc, ok := sr.domains[domainID]
	if !ok {
		dc = &DomainContribution{}
		sr.domains[domainID] = dc
	}
	return dc
}
