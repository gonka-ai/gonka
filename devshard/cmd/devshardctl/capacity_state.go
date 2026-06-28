package main

import (
	"math"
	"sort"
	"strings"
	"sync"
)

// CapacityState tracks per-host weight, per-escrow membership, and the
// per-host availability signals (PoC preservation, reactive throttle)
// that drive capacity-aware routing. It exposes:
//
//   - W(e)   per-escrow effective weight, used by the gateway picker to
//     route the next request to the escrow with the most spare
//     effective capacity.
//   - W_tot  gateway-wide weight using current raw poc_weight capacity
//     (each host counted once, no escrow split). Reflects PoC-induced
//     capacity reductions.
//   - W_ref  gateway-wide baseline using full (steady-state) raw
//     poc_weight capacity, ignoring throttle and PoC. Recomputed live; the
//     scale factor is W_tot / W_ref.
//
// Per-host weights are raw poc_weight totals kept in two flavors -- full
// (last value seen during steady-state Inference) and current (latest
// poll, possibly reduced during PoC). The phase-gate decides which fields
// to update via SetHostWeights' pocActive argument.
//
// Availability is binary: a host is either available (full weight
// counts toward W(e)) or unavailable (drops out of W(e) and the picker
// routes around it). The two unavailability signals are PoC
// preservation (phase-level, set in bulk on PoC entry) and the live
// availability callback (reactive, queried on every read so a 503
// drops W(e) on the very next request without waiting for a phase
// poll).
//
// All inputs are pushed in by external owners (chain capacity ingestion,
// PoC machinery, ParticipantRequestLimiter). The state never blocks on
// I/O and is safe for concurrent use.
type CapacityState struct {
	mu sync.RWMutex

	// Per-host raw poc_weight capacity. fullWeights is the last value seen
	// outside PoC (steady-state baseline). currentWeights is the
	// latest poll (matches fullWeights outside PoC, may be reduced
	// during PoC). Only the keys we have actually observed appear in
	// each map; absence means "unknown" -> fall back to 1.0.
	fullWeights    map[string]float64
	currentWeights map[string]float64

	// Optional model-specific views of the same raw weights. When a caller
	// asks for a known model, these maps let the picker use only the
	// capacity that the chain reports for that model.
	fullWeightsByModel    map[string]map[string]float64
	currentWeightsByModel map[string]map[string]float64

	// PoC preservation. nil means "not yet loaded" (treat every host
	// as preserved so we don't wedge to zero capacity on a missed
	// poll). Empty map means "PoC active and nobody is preserved"
	// (every host blocked by PoC). A non-empty map enumerates the
	// preserved set.
	pocPreserved map[string]struct{}

	// Live availability source consulted on every W(e) computation so
	// a 503 (or recovery) shrinks/restores W(e) on the very next
	// picker call rather than waiting for the next phase poll. nil
	// means "no reactive signal wired" -> treat every host as
	// available. The callback is invoked while holding m.mu (read),
	// so it MUST NOT take any lock that could call back into the
	// state.
	liveAvailable func(host string) bool

	// Per-escrow membership: escrowID -> hostKey -> slot count in this
	// escrow. Built once per runtime at construction and refreshed on
	// admin add/remove.
	escrowMembership map[string]map[string]int

	// Cache of total slots per host across all escrows. Recomputed
	// whenever escrowMembership changes.
	hostTotalSlots map[string]int
}

// NewCapacityState returns an empty state. All inputs are pushed via
// the Set* methods; until they arrive every host is treated as available
// at weight 1, so the picker degrades gracefully to round-robin behavior.
func NewCapacityState() *CapacityState {
	return &CapacityState{
		fullWeights:           map[string]float64{},
		currentWeights:        map[string]float64{},
		fullWeightsByModel:    map[string]map[string]float64{},
		currentWeightsByModel: map[string]map[string]float64{},
		escrowMembership:      map[string]map[string]int{},
		hostTotalSlots:        map[string]int{},
	}
}

// SetEscrowMembership replaces the per-escrow slot counts for one
// escrow. slotCounts maps host participant key to the number of slots
// the host occupies in this escrow.
func (m *CapacityState) SetEscrowMembership(escrowID string, slotCounts map[string]int) {
	if m == nil {
		return
	}
	clean := make(map[string]int, len(slotCounts))
	for k, c := range slotCounts {
		if k == "" || c <= 0 {
			continue
		}
		clean[k] = c
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(clean) == 0 {
		delete(m.escrowMembership, escrowID)
	} else {
		m.escrowMembership[escrowID] = clean
	}
	m.recomputeTotalSlotsLocked()
}

// RemoveEscrow drops a runtime's membership entirely.
func (m *CapacityState) RemoveEscrow(escrowID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.escrowMembership, escrowID)
	m.recomputeTotalSlotsLocked()
}

func (m *CapacityState) recomputeTotalSlotsLocked() {
	totals := map[string]int{}
	for _, slots := range m.escrowMembership {
		for k, c := range slots {
			totals[k] += c
		}
	}
	m.hostTotalSlots = totals
}

// SetHostWeights replaces per-host raw poc_weight capacity. The pocActive flag
// determines whether the call is treated as a steady-state observation
// (updates BOTH fullWeights and currentWeights) or a PoC-time
// observation (updates only currentWeights, leaving fullWeights at the
// last steady-state value).
//
// This dual-storage is what lets ScaleFactor = W_tot/W_ref be computed
// live without freezing anything: W_ref always sums fullWeights and
// W_tot always sums currentWeights, so during PoC the ratio naturally
// tracks how much capacity the preservation/throttle picture has
// shaved off the steady-state baseline.
//
// Empty keys and negative weights are ignored.
func (m *CapacityState) SetHostWeights(weights map[string]float64, pocActive bool) {
	if m == nil {
		return
	}
	clean := make(map[string]float64, len(weights))
	for k, w := range weights {
		if k == "" || w < 0 {
			continue
		}
		clean[k] = w
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentWeights = clean
	if !pocActive {
		// Outside PoC, current == full by definition. Replace
		// fullWeights with a copy so a future SetHostWeights(_, true)
		// can mutate currentWeights independently.
		full := make(map[string]float64, len(clean))
		for k, w := range clean {
			full[k] = w
		}
		m.fullWeights = full
	}
}

// SetHostWeightsByModel replaces per-model, per-host raw poc_weight capacity. It
// follows the same baseline/current split as SetHostWeights.
func (m *CapacityState) SetHostWeightsByModel(weights map[string]map[string]float64, pocActive bool) {
	if m == nil {
		return
	}
	clean := cleanModelWeights(weights)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentWeightsByModel = clean
	if !pocActive {
		m.fullWeightsByModel = cloneModelWeights(clean)
	}
}

func cleanModelWeights(weights map[string]map[string]float64) map[string]map[string]float64 {
	clean := make(map[string]map[string]float64, len(weights))
	for rawModel, hostWeights := range weights {
		model := strings.TrimSpace(rawModel)
		if model == "" {
			continue
		}
		cleanHosts := make(map[string]float64, len(hostWeights))
		for host, weight := range hostWeights {
			if host == "" || weight < 0 {
				continue
			}
			cleanHosts[host] = weight
		}
		if len(cleanHosts) > 0 {
			clean[model] = cleanHosts
		}
	}
	return clean
}

func cloneModelWeights(weights map[string]map[string]float64) map[string]map[string]float64 {
	clone := make(map[string]map[string]float64, len(weights))
	for model, hostWeights := range weights {
		cloneHosts := make(map[string]float64, len(hostWeights))
		for host, weight := range hostWeights {
			cloneHosts[host] = weight
		}
		clone[model] = cloneHosts
	}
	return clone
}

// SetPoCPreserved updates the preserved-host set. Pass nil to mark the
// preserved set as "not yet loaded" (treat every host as preserved so
// we don't wedge to zero capacity on a missed poll). Pass a non-nil
// (possibly empty) slice to enumerate the preserved set; hosts not in
// the slice are treated as PoC-blocked.
func (m *CapacityState) SetPoCPreserved(keys []string) {
	if m == nil {
		return
	}
	var preserved map[string]struct{}
	if keys != nil {
		preserved = make(map[string]struct{}, len(keys))
		for _, k := range keys {
			if k == "" {
				continue
			}
			preserved[k] = struct{}{}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pocPreserved = preserved
}

// SetLiveAvailable installs a function that returns true iff the host
// is currently available for routing. When set, it is consulted on
// every W(e)/W_tot computation so a 503 (or recovery) shrinks/restores
// the picker's view of capacity on the very next request rather than
// waiting for the next phase poll. Pass nil to clear (every host
// treated as available).
//
// The callback is invoked while holding m.mu (read), so it MUST NOT
// take any lock that could call back into this state. The
// ParticipantRequestLimiter's IsAvailable method satisfies this.
func (m *CapacityState) SetLiveAvailable(src func(host string) bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.liveAvailable = src
}

// hostAvailableLocked returns whether the host can currently serve
// traffic, combining PoC preservation and live throttle signals. When
// the preserved set has not yet loaded (pocPreserved == nil) every
// host is assumed preserved.
func (m *CapacityState) hostAvailableLocked(host string) bool {
	if m.pocPreserved != nil {
		if _, ok := m.pocPreserved[host]; !ok {
			return false
		}
	}
	if m.liveAvailable != nil && !m.liveAvailable(host) {
		return false
	}
	return true
}

// hostCurrentWeightLocked returns the current raw poc_weight capacity for the
// host or 1.0 if the state has no entry (best-effort fallback so
// routing still works before the first chain fetch lands).
func (m *CapacityState) hostCurrentWeightLocked(host string) float64 {
	if w, ok := m.currentWeights[host]; ok {
		return w
	}
	return 1.0
}

func (m *CapacityState) hostCurrentWeightForModelLocked(host, model string) float64 {
	if model != "" {
		if weights, ok := m.currentWeightsByModel[model]; ok {
			if weight, ok := weights[host]; ok {
				return weight
			}
			return 0
		}
	}
	return m.hostCurrentWeightLocked(host)
}

// hostFullWeightLocked returns the steady-state raw poc_weight capacity for the
// host or 1.0 if no Inference-phase observation has landed yet.
func (m *CapacityState) hostFullWeightLocked(host string) float64 {
	if w, ok := m.fullWeights[host]; ok {
		return w
	}
	return 1.0
}

func (m *CapacityState) hostFullWeightForModelLocked(host, model string) float64 {
	if model != "" {
		if weights, ok := m.fullWeightsByModel[model]; ok {
			if weight, ok := weights[host]; ok {
				return weight
			}
			return 0
		}
	}
	return m.hostFullWeightLocked(host)
}

// EscrowWeight computes W(e) = sum over h in e of
// w_current(h) * share(h,e) * available(h), where
// share(h,e) = slots(h,e) / total_slots(h) splits a host across the
// escrows it serves so a shared host does not double-count.
//
// Returns 0 for unknown escrows so the picker treats them as unusable
// rather than infinitely cheap.
func (m *CapacityState) EscrowWeight(escrowID string) float64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.escrowWeightLocked(escrowID, "")
}

// EscrowWeightForModel computes W(e) using model-specific raw poc_weight capacity
// when they have been observed for the requested model.
func (m *CapacityState) EscrowWeightForModel(escrowID, model string) float64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.escrowWeightLocked(escrowID, model)
}

func (m *CapacityState) escrowWeightLocked(escrowID, model string) float64 {
	slots, ok := m.escrowMembership[escrowID]
	if !ok || len(slots) == 0 {
		return 0
	}
	var sum float64
	for host, count := range slots {
		total := m.hostTotalSlots[host]
		if total <= 0 {
			continue
		}
		if !m.hostAvailableLocked(host) {
			continue
		}
		share := float64(count) / float64(total)
		sum += m.hostCurrentWeightForModelLocked(host, model) * share
	}
	return sum
}

// TotalWeight returns W_tot. Each host is counted ONCE (no escrow share
// split) so a host shared across N escrows contributes its full weight
// to the gateway-wide picture rather than N times its split. Reflects
// availability (PoC preservation + live throttle) so the ratio against
// BaselineWeight tracks current effective capacity.
func (m *CapacityState) TotalWeight() float64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalWeightLocked("")
}

func (m *CapacityState) TotalWeightForModel(model string) float64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalWeightLocked(model)
}

func (m *CapacityState) totalWeightLocked(model string) float64 {
	var sum float64
	for host := range m.hostTotalSlots {
		if !m.hostAvailableLocked(host) {
			continue
		}
		sum += m.hostCurrentWeightForModelLocked(host, model)
	}
	return sum
}

// BaselineWeight returns W_ref: the gateway-wide weight computed from
// the last steady-state full weights, IGNORING availability. This is
// the denominator of the scale factor and represents "capacity if
// every host were healthy and outside PoC". Recomputed live; no
// freezing required (full weights are only updated outside PoC, see
// SetHostWeights).
func (m *CapacityState) BaselineWeight() float64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.baselineWeightLocked("")
}

func (m *CapacityState) BaselineWeightForModel(model string) float64 {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.baselineWeightLocked(model)
}

func (m *CapacityState) baselineWeightLocked(model string) float64 {
	var sum float64
	for host := range m.hostTotalSlots {
		sum += m.hostFullWeightForModelLocked(host, model)
	}
	return sum
}

// ScaleFactor returns W_tot / W_ref clamped to [0, 1]. Returns 1.0
// when the baseline is zero (no full weights observed yet) so callers
// default to the configured caps.
func (m *CapacityState) ScaleFactor() float64 {
	if m == nil {
		return 1
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scaleFactorLocked("")
}

func (m *CapacityState) ScaleFactorForModel(model string) float64 {
	if m == nil {
		return 1
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scaleFactorLocked(model)
}

// ScaleFactorAcrossModels returns aggregate available capacity divided
// by aggregate baseline capacity across all observed models. If no
// model-specific baseline is loaded, it falls back to the host-level
// scale factor.
func (m *CapacityState) ScaleFactorAcrossModels() float64 {
	if m == nil {
		return 1
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scaleFactorAcrossModelsLocked()
}

// LimitShareForModel returns the fraction of the configured global
// gateway limit that should be available to this model:
// W_current(model) / W_ref(all models). This gives each model an
// independent counter while keeping one operator-facing global setting.
func (m *CapacityState) LimitShareForModel(model string) float64 {
	if m == nil {
		return 1
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	model = strings.TrimSpace(model)
	if model == "" || len(m.fullWeightsByModel) == 0 {
		return m.scaleFactorLocked("")
	}
	baseline := m.baselineWeightAcrossModelsLocked()
	if baseline <= 0 {
		return 1
	}
	return clampScale(m.totalWeightLocked(model) / baseline)
}

func (m *CapacityState) scaleFactorLocked(model string) float64 {
	baseline := m.baselineWeightLocked(model)
	if baseline <= 0 {
		return 1
	}
	current := m.totalWeightLocked(model)
	return clampScale(current / baseline)
}

func (m *CapacityState) scaleFactorAcrossModelsLocked() float64 {
	if len(m.fullWeightsByModel) == 0 {
		return m.scaleFactorLocked("")
	}
	baseline := m.baselineWeightAcrossModelsLocked()
	if baseline <= 0 {
		return 1
	}
	return clampScale(m.totalWeightAcrossModelsLocked() / baseline)
}

func (m *CapacityState) baselineWeightAcrossModelsLocked() float64 {
	var sum float64
	for model := range m.fullWeightsByModel {
		sum += m.baselineWeightLocked(model)
	}
	return sum
}

func (m *CapacityState) totalWeightAcrossModelsLocked() float64 {
	var sum float64
	for model := range m.fullWeightsByModel {
		sum += m.totalWeightLocked(model)
	}
	return sum
}

func clampScale(scale float64) float64 {
	if math.IsNaN(scale) || math.IsInf(scale, 0) {
		return 1
	}
	if scale < 0 {
		scale = 0
	}
	if scale > 1 {
		scale = 1
	}
	return scale
}

// CapacitySnapshot is a read-only view of the state used by metrics
// and admin endpoints.
type CapacitySnapshot struct {
	TotalWeight              float64
	BaselineWeight           float64
	ScaleFactor              float64
	EscrowWeights            map[string]float64
	HostCount                int
	AvailableHostCount       int
	UnavailableHostCount     int
	CurrentWeightMatched     int
	CurrentWeightFallback    int
	BaselineWeightMatched    int
	BaselineWeightFallback   int
	ObservedCurrentWeightKey int
	ObservedFullWeightKey    int
}

func (m *CapacityState) Models() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]struct{}, len(m.fullWeightsByModel)+len(m.currentWeightsByModel))
	for model := range m.fullWeightsByModel {
		seen[model] = struct{}{}
	}
	for model := range m.currentWeightsByModel {
		seen[model] = struct{}{}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// Snapshot copies the current state.
func (m *CapacityState) Snapshot() CapacitySnapshot {
	if m == nil {
		return CapacitySnapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	weights := make(map[string]float64, len(m.escrowMembership))
	for id := range m.escrowMembership {
		weights[id] = m.escrowWeightLocked(id, "")
	}
	available, unavailable := 0, 0
	currentMatched, currentFallback := 0, 0
	baselineMatched, baselineFallback := 0, 0
	for host := range m.hostTotalSlots {
		if m.hostAvailableLocked(host) {
			available++
		} else {
			unavailable++
		}
		if _, ok := m.currentWeights[host]; ok {
			currentMatched++
		} else {
			currentFallback++
		}
		if _, ok := m.fullWeights[host]; ok {
			baselineMatched++
		} else {
			baselineFallback++
		}
	}
	return CapacitySnapshot{
		TotalWeight:              m.totalWeightLocked(""),
		BaselineWeight:           m.baselineWeightLocked(""),
		ScaleFactor:              m.scaleFactorLocked(""),
		EscrowWeights:            weights,
		HostCount:                len(m.hostTotalSlots),
		AvailableHostCount:       available,
		UnavailableHostCount:     unavailable,
		CurrentWeightMatched:     currentMatched,
		CurrentWeightFallback:    currentFallback,
		BaselineWeightMatched:    baselineMatched,
		BaselineWeightFallback:   baselineFallback,
		ObservedCurrentWeightKey: len(m.currentWeights),
		ObservedFullWeightKey:    len(m.fullWeights),
	}
}

// HasEscrow reports whether the state has membership data for the
// given escrow. Used by callers (e.g. the gateway picker) to decide
// whether a returned weight of 0 means "no info yet" or "explicitly
// unavailable".
func (m *CapacityState) HasEscrow(escrowID string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.escrowMembership[escrowID]
	return ok
}

// EscrowIDs returns a stable ordering of registered escrows. Useful for
// metrics that need a deterministic label set.
func (m *CapacityState) EscrowIDs() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.escrowMembership))
	for id := range m.escrowMembership {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
