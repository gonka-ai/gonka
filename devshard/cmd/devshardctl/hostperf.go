package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"devshard/user"
)

var (
	PerfWindowSize        = 256
	ParticipantPerfWindow = 60 * time.Minute
)

func DefaultPerfSettings() PerfSettings {
	return PerfSettings{
		SampleSize: PerfWindowSize,
		WindowMS:   ParticipantPerfWindow.Milliseconds(),
	}
}

func ApplyPerfSettings(settings PerfSettings) {
	defaults := PerfSettings{SampleSize: 256, WindowMS: int64(time.Hour / time.Millisecond)}
	if settings.SampleSize <= 0 {
		settings.SampleSize = defaults.SampleSize
	}
	if settings.WindowMS <= 0 {
		settings.WindowMS = defaults.WindowMS
	}
	PerfWindowSize = settings.SampleSize
	ParticipantPerfWindow = time.Duration(settings.WindowMS) * time.Millisecond
}

type RequestSample struct {
	HostIdx        int
	ParticipantKey string
	Responsive     bool
	SendTime       time.Time
	ReceiptTime    time.Time // zero if no receipt
	FirstToken     time.Time // zero if no tokens
	TotalTime      time.Duration
	InputTokens    uint64
}

func (s RequestSample) ReceiptMs() float64 {
	if s.ReceiptTime.IsZero() || s.SendTime.IsZero() {
		return 0
	}
	return float64(s.ReceiptTime.Sub(s.SendTime).Milliseconds())
}

// CTTFL = (firstTokenTime - receiptTime) / inputTokens
func (s RequestSample) CTTFL() float64 {
	if s.FirstToken.IsZero() || s.ReceiptTime.IsZero() || s.InputTokens == 0 {
		return 0
	}
	gap := s.FirstToken.Sub(s.ReceiptTime)
	if gap <= 0 {
		return 0
	}
	return float64(gap.Milliseconds()) / float64(s.InputTokens)
}

type hostRing struct {
	samples []RequestSample
	pos     int
	count   int
}

func (r *hostRing) add(s RequestSample) {
	r.ensureSize()
	r.samples[r.pos] = s
	r.pos = (r.pos + 1) % len(r.samples)
	if r.count < len(r.samples) {
		r.count++
	}
}

func (r *hostRing) ensureSize() {
	size := PerfWindowSize
	if size <= 0 {
		size = 256
	}
	if len(r.samples) == size {
		return
	}
	old := r.all()
	if len(old) > size {
		old = old[len(old)-size:]
	}
	r.samples = make([]RequestSample, size)
	copy(r.samples, old)
	r.count = len(old)
	if size > 0 {
		r.pos = r.count % size
	} else {
		r.pos = 0
	}
}

func (r *hostRing) all() []RequestSample {
	if r.count == 0 || len(r.samples) == 0 {
		return nil
	}
	out := make([]RequestSample, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.pos - r.count + i + len(r.samples)) % len(r.samples)
		out[i] = r.samples[idx]
	}
	return out
}

func (r *hostRing) hasHostIdx(hostIdx int) bool {
	for _, sample := range r.all() {
		if sample.HostIdx == hostIdx {
			return true
		}
	}
	return false
}

type HostPerfStats struct {
	ParticipantKey   string  `json:"participant_key,omitempty"`
	HostIdx          int     `json:"host_idx"`
	TotalSamples     int     `json:"total_samples"`
	FailureSamples   int     `json:"failure_samples"`
	ResponsiveRate   float64 `json:"responsive_rate"`
	AvgReceiptTimeMs float64 `json:"avg_receipt_time_ms"`
	AvgCTTFL         float64 `json:"avg_cttfl"`
	AvgTotalTimeMs   float64 `json:"avg_total_time_ms"`
	WindowStart      string  `json:"window_start,omitempty"`
}

func (r *hostRing) stats(participantKey string, hostIdx int, windowStart time.Time) HostPerfStats {
	base := HostPerfStats{
		ParticipantKey: participantKey,
		HostIdx:        hostIdx,
	}
	if !windowStart.IsZero() {
		base.WindowStart = windowStart.Format(time.RFC3339Nano)
	}
	if r.count == 0 {
		return base
	}

	var responsive int
	var receiptSum, cttflSum, totalSum float64
	var receiptN, cttflN, totalN int
	var total int

	for _, s := range r.all() {
		if !windowStart.IsZero() && s.SendTime.Before(windowStart) {
			continue
		}
		total++
		if s.HostIdx >= 0 {
			base.HostIdx = s.HostIdx
		}
		if s.Responsive {
			responsive++
		}
		if rm := s.ReceiptMs(); rm > 0 {
			receiptSum += rm
			receiptN++
		}
		if c := s.CTTFL(); c > 0 && !math.IsNaN(c) && !math.IsInf(c, 0) {
			cttflSum += c
			cttflN++
		}
		if s.TotalTime > 0 {
			totalSum += float64(s.TotalTime.Milliseconds())
			totalN++
		}
	}

	if total == 0 {
		return base
	}
	st := base
	st.TotalSamples = total
	st.FailureSamples = total - responsive
	st.ResponsiveRate = float64(responsive) / float64(total)
	if receiptN > 0 {
		st.AvgReceiptTimeMs = receiptSum / float64(receiptN)
	}
	if cttflN > 0 {
		st.AvgCTTFL = cttflSum / float64(cttflN)
	}
	if totalN > 0 {
		st.AvgTotalTimeMs = totalSum / float64(totalN)
	}
	return st
}

// HostInvolvement describes one host's participation in a user request.
type HostInvolvement struct {
	HostIdx         int     `json:"host_idx"`
	ParticipantKey  string  `json:"participant_key,omitempty"`
	Nonce           uint64  `json:"nonce"`
	OutputChunks    int64   `json:"output_chunks"`
	ReceiptTimeMs   float64 `json:"receipt_time_ms"`
	FirstTokenMs    float64 `json:"first_token_ms"`
	TotalTimeMs     float64 `json:"total_time_ms"`
	Responsive      bool    `json:"responsive"`
	Finished        bool    `json:"finished"`
	Winner          bool    `json:"winner"`
	ExcludePairwise bool    `json:"exclude_pairwise,omitempty"`
}

// RequestRecord logs a single user-facing inference request.
type RequestRecord struct {
	Timestamp     time.Time         `json:"timestamp"`
	Model         string            `json:"model,omitempty"`
	InputTokens   uint64            `json:"input_tokens"`
	WinnerHostIdx int               `json:"winner_host_idx"`
	WinnerNonce   uint64            `json:"winner_nonce"`
	Decision      string            `json:"decision"`
	Hosts         []HostInvolvement `json:"hosts"`
}

const (
	requestLogSize             = 4096
	firstTokenBucketSampleSize = 100
)

type requestRing struct {
	records [requestLogSize]RequestRecord
	pos     int
	count   int
}

func (r *requestRing) add(rec RequestRecord) {
	r.records[r.pos] = rec
	r.pos = (r.pos + 1) % requestLogSize
	if r.count < requestLogSize {
		r.count++
	}
}

func (r *requestRing) all() []RequestRecord {
	if r.count == 0 {
		return nil
	}
	result := make([]RequestRecord, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.pos - r.count + i + requestLogSize) % requestLogSize
		result[i] = r.records[idx]
	}
	return result
}

type PerfTracker struct {
	mu                sync.RWMutex
	hosts             map[string]*hostRing
	requests          requestRing
	firstTokenBuckets map[string]*firstTokenBucketRing
	contextLimits     map[string]uint64 // participant_key -> observed max context length
	toolUnsupported   map[string]bool   // participant_key -> host reported vLLM tool-choice support is disabled
	pairwise          *PairwiseTracker
	store             *PerfStore
}

func NewPerfTracker(store *PerfStore) *PerfTracker {
	pt := &PerfTracker{
		hosts:             make(map[string]*hostRing),
		firstTokenBuckets: make(map[string]*firstTokenBucketRing),
		contextLimits:     make(map[string]uint64),
		toolUnsupported:   make(map[string]bool),
		pairwise:          NewPairwiseTracker(),
		store:             store,
	}
	if store != nil {
		pt.loadFromStore()
	}
	return pt
}

func (t *PerfTracker) loadFromStore() {
	samples, err := t.store.LoadSamples()
	if err != nil {
		log.Printf("perf: failed to load samples: %v", err)
		return
	}
	for _, s := range samples {
		key := perfSampleKey(s)
		if key == "" {
			continue
		}
		ring, ok := t.hosts[key]
		if !ok {
			ring = &hostRing{}
			t.hosts[key] = ring
		}
		ring.add(s)
	}

	records, err := t.store.LoadRequests()
	if err != nil {
		log.Printf("perf: failed to load requests: %v", err)
		return
	}
	for _, r := range records {
		t.requests.add(r)
		t.recordFirstTokenSampleLocked(r)
		if t.pairwise != nil {
			t.pairwise.RecordRequest(r)
		}
	}

	if len(samples) > 0 || len(records) > 0 {
		log.Printf("perf: restored %d host samples, %d request records from disk", len(samples), len(records))
	}
}

func (t *PerfTracker) addLoadedSamples(samples []RequestSample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range samples {
		key := perfSampleKey(s)
		if key == "" {
			continue
		}
		ring, ok := t.hosts[key]
		if !ok {
			ring = &hostRing{}
			t.hosts[key] = ring
		}
		ring.add(s)
	}
}

func (t *PerfTracker) BackfillLegacyEscrowSamples(sourceEscrow, sourcePath string, participantKeys []string) error {
	if t == nil || t.store == nil {
		return nil
	}
	samples, err := t.store.BackfillLegacyEscrowSamples(sourceEscrow, sourcePath, participantKeys)
	if err != nil {
		return err
	}
	t.addLoadedSamples(samples)
	return nil
}

func (t *PerfTracker) ResizeRings() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ring := range t.hosts {
		ring.ensureSize()
	}
}

func (t *PerfTracker) Record(s RequestSample) {
	if s.SendTime.IsZero() {
		s.SendTime = time.Now()
	}
	key := perfSampleKey(s)
	if key == "" {
		return
	}
	t.mu.Lock()
	ring, ok := t.hosts[key]
	if !ok {
		ring = &hostRing{}
		t.hosts[key] = ring
	}
	ring.add(s)
	t.mu.Unlock()

	if t.store != nil {
		if err := t.store.InsertSample(s); err != nil {
			log.Printf("perf: persist sample: %v", err)
		}
	}
}

func (t *PerfTracker) Stats(hostIdx int) HostPerfStats {
	key := legacyHostPerfKey(hostIdx)
	now := time.Now()
	t.mu.RLock()
	if _, ok := t.hosts[key]; !ok {
		for participantKey, ring := range t.hosts {
			if ring.hasHostIdx(hostIdx) {
				t.mu.RUnlock()
				return t.statsForKey(participantKey, hostIdx, now)
			}
		}
	}
	t.mu.RUnlock()
	return t.statsForKey(key, hostIdx, now)
}

func (t *PerfTracker) StatsForParticipant(participantKey string) HostPerfStats {
	return t.statsForKey(participantKey, -1, time.Now())
}

func (t *PerfTracker) statsForKey(key string, fallbackHostIdx int, now time.Time) HostPerfStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ring, ok := t.hosts[key]
	windowStart := participantPerfWindowStart(key, now)
	if !ok {
		st := HostPerfStats{ParticipantKey: key, HostIdx: fallbackHostIdx}
		if !windowStart.IsZero() {
			st.WindowStart = windowStart.Format(time.RFC3339Nano)
		}
		return st
	}
	return ring.stats(key, fallbackHostIdx, windowStart)
}

func (t *PerfTracker) AllStats() []HostPerfStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]HostPerfStats, 0, len(t.hosts))
	now := time.Now()
	for key, ring := range t.hosts {
		result = append(result, ring.stats(key, -1, participantPerfWindowStart(key, now)))
	}
	return result
}

// EstimatedTimeMs returns estimated total time for an inference.
// Uses: receiptTime + cTTFL * inputTokens.
// Returns 0 if insufficient data.
func (t *PerfTracker) EstimatedTimeMs(hostIdx int, inputTokens uint64) float64 {
	st := t.Stats(hostIdx)
	return estimatedTimeFromStats(st, inputTokens)
}

func (t *PerfTracker) EstimatedTimeMsForParticipant(participantKey string, inputTokens uint64) float64 {
	st := t.StatsForParticipant(participantKey)
	return estimatedTimeFromStats(st, inputTokens)
}

func estimatedTimeFromStats(st HostPerfStats, inputTokens uint64) float64 {
	if st.TotalSamples == 0 || st.AvgReceiptTimeMs == 0 {
		return 0
	}
	return st.AvgReceiptTimeMs + st.AvgCTTFL*float64(inputTokens)
}

func (t *PerfTracker) RecordRequest(rec RequestRecord) {
	t.mu.Lock()
	t.requests.add(rec)
	t.recordFirstTokenSampleLocked(rec)
	t.mu.Unlock()
	if t.pairwise != nil {
		t.pairwise.RecordRequest(rec)
	}

	if t.store != nil {
		if err := t.store.InsertRequest(rec); err != nil {
			log.Printf("perf: persist request: %v", err)
		}
	}
}

func (t *PerfTracker) RecentRequests() []RequestRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.requests.all()
}

func (t *PerfTracker) FirstTokenFallbackDelay(model string, inputTokens uint64) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	key := firstTokenBucketKey(model, inputTokens)
	t.mu.RLock()
	ring := t.firstTokenBuckets[key]
	if ring == nil || ring.count < firstTokenBucketSampleSize {
		t.mu.RUnlock()
		return 0, false
	}
	samples := ring.all()
	t.mu.RUnlock()
	delay := percentileDuration(samples, 0.95)
	if delay <= 0 {
		return 0, false
	}
	return delay, true
}

// RecordContextLimit stores the observed maximum context length for a
// participant, as reported by the host in a context-length error response.
// Only updates if the new limit differs from the previously recorded value.
func (t *PerfTracker) RecordContextLimit(participantKey string, maxTokens uint64) {
	if t == nil || participantKey == "" || maxTokens == 0 {
		return
	}
	t.mu.Lock()
	prev, exists := t.contextLimits[participantKey]
	if !exists || prev != maxTokens {
		t.contextLimits[participantKey] = maxTokens
		log.Printf("perf: recorded context_limit participant_key=%s max_tokens=%d prev=%d", participantKey, maxTokens, prev)
	}
	t.mu.Unlock()
}

func (t *PerfTracker) RecordToolUnsupported(participantKey string) {
	if t == nil || participantKey == "" {
		return
	}
	t.mu.Lock()
	if !t.toolUnsupported[participantKey] {
		t.toolUnsupported[participantKey] = true
		log.Printf("perf: recorded tool_unsupported participant_key=%s", participantKey)
	}
	t.mu.Unlock()
}

// ContextLimits returns a snapshot of all observed host context length limits.
func (t *PerfTracker) ContextLimits() map[string]uint64 {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make(map[string]uint64, len(t.contextLimits))
	for k, v := range t.contextLimits {
		result[k] = v
	}
	return result
}

func (t *PerfTracker) ToolUnsupported() map[string]bool {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make(map[string]bool, len(t.toolUnsupported))
	for k, v := range t.toolUnsupported {
		result[k] = v
	}
	return result
}

func (t *PerfTracker) HostCannotServeRequest(participantKey string, params user.InferenceParams) (string, bool) {
	if t == nil || participantKey == "" {
		return "", false
	}
	requiresTools := requestRequiresTools(params)
	t.mu.RLock()
	defer t.mu.RUnlock()
	if requiresTools && t.toolUnsupported[participantKey] {
		return "tool_choice_unsupported", true
	}
	if limit := t.contextLimits[participantKey]; limit > 0 && params.ContextTotalHint > limit {
		return "context_limit_exceeded", true
	}
	return "", false
}

func (t *PerfTracker) AllKnownToolUnsupported(participantKeys []string) bool {
	if t == nil || len(participantKeys) == 0 {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, key := range participantKeys {
		if key == "" {
			continue
		}
		if !t.toolUnsupported[key] {
			return false
		}
	}
	return true
}

func (t *PerfTracker) PairwiseSummaries() []PairwiseSummary {
	if t == nil || t.pairwise == nil {
		return nil
	}
	return t.pairwise.Summaries()
}

func (t *PerfTracker) IsUnresponsive(hostIdx int) bool {
	st := t.Stats(hostIdx)
	return statsUnresponsive(st)
}

func (t *PerfTracker) IsUnresponsiveParticipant(participantKey string) bool {
	st := t.StatsForParticipant(participantKey)
	return statsUnresponsive(st)
}

func statsUnresponsive(st HostPerfStats) bool {
	if st.TotalSamples == 0 {
		return false
	}
	return st.ResponsiveRate < UnresponsiveThreshold
}

func (t *PerfTracker) ParticipantFailureThresholdExceeded(participantKey string) bool {
	st := t.StatsForParticipant(participantKey)
	if st.TotalSamples == 0 {
		return false
	}
	if st.TotalSamples < 100 {
		return st.FailureSamples > 1
	}
	return float64(st.FailureSamples)/float64(st.TotalSamples) > 0.01
}

func perfSampleKey(s RequestSample) string {
	if s.ParticipantKey != "" {
		return s.ParticipantKey
	}
	return legacyHostPerfKey(s.HostIdx)
}

func legacyHostPerfKey(hostIdx int) string {
	return fmt.Sprintf("host:%d", hostIdx)
}

type firstTokenBucketRing struct {
	samples [firstTokenBucketSampleSize]time.Duration
	pos     int
	count   int
}

func (r *firstTokenBucketRing) add(d time.Duration) {
	if d <= 0 {
		return
	}
	r.samples[r.pos] = d
	r.pos = (r.pos + 1) % len(r.samples)
	if r.count < len(r.samples) {
		r.count++
	}
}

func (r *firstTokenBucketRing) all() []time.Duration {
	if r == nil || r.count == 0 {
		return nil
	}
	out := make([]time.Duration, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.pos - r.count + i + len(r.samples)) % len(r.samples)
		out[i] = r.samples[idx]
	}
	return out
}

func (t *PerfTracker) recordFirstTokenSampleLocked(rec RequestRecord) {
	if t == nil || rec.Model == "" || rec.InputTokens == 0 {
		return
	}
	for _, host := range rec.Hosts {
		if !host.Winner || !host.Finished || !host.Responsive || host.FirstTokenMs <= 0 {
			continue
		}
		key := firstTokenBucketKey(rec.Model, rec.InputTokens)
		ring := t.firstTokenBuckets[key]
		if ring == nil {
			ring = &firstTokenBucketRing{}
			t.firstTokenBuckets[key] = ring
		}
		ring.add(time.Duration(host.FirstTokenMs) * time.Millisecond)
		return
	}
}

func firstTokenBucketKey(model string, inputTokens uint64) string {
	return model + "\x00" + firstTokenInputBucket(inputTokens)
}

func firstTokenInputBucket(inputTokens uint64) string {
	switch {
	case inputTokens < 2_000:
		return "lt_2k"
	case inputTokens < 4_000:
		return "2k_4k"
	case inputTokens < 8_000:
		return "4k_8k"
	case inputTokens < 16_000:
		return "8k_16k"
	case inputTokens < 32_000:
		return "16k_32k"
	case inputTokens < 64_000:
		return "32k_64k"
	case inputTokens < 128_000:
		return "64k_128k"
	case inputTokens < 256_000:
		return "128k_256k"
	default:
		return "gte_256k"
	}
}

func percentileDuration(values []time.Duration, q float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func participantPerfWindowStart(participantKey string, now time.Time) time.Time {
	if ParticipantPerfWindow <= 0 || participantKey == "" || now.IsZero() {
		return time.Time{}
	}
	windowNanos := ParticipantPerfWindow.Nanoseconds()
	if windowNanos <= 0 {
		return time.Time{}
	}
	offset := participantPerfWindowOffset(participantKey)
	shifted := now.Add(-offset)
	truncated := shifted.Truncate(ParticipantPerfWindow)
	return truncated.Add(offset)
}

func participantPerfWindowOffset(participantKey string) time.Duration {
	if ParticipantPerfWindow <= 0 || participantKey == "" {
		return 0
	}
	windowNanos := ParticipantPerfWindow.Nanoseconds()
	if windowNanos <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(participantKey))
	return time.Duration(int64(h.Sum64() % uint64(windowNanos)))
}
