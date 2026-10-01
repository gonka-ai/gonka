package state

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"devshard/types"
)

// rootInputInferenceCap is how many inference rows a divergence response may
// carry. Larger sets are represented by inferences_hash and the counts.
const rootInputInferenceCap = 8

// RootInputs is the host's post-apply view of every value that enters the
// state root, plus the component hashes. The gateway compares this with its
// own view to see which input diverged.
type RootInputs struct {
	Nonce            uint64 `json:"nonce"`
	LatestNonce      uint64 `json:"latest_nonce"`
	Balance          uint64 `json:"balance"`
	Fees             uint64 `json:"fees"`
	Phase            uint8  `json:"phase"`
	Version          string `json:"version"`
	LiveInferences   int    `json:"live_inferences"`
	SealedInferences int    `json:"sealed_inferences"`
	SealedAcc        string `json:"sealed_acc"`

	ForcedStart         uint64 `json:"hs_forced_start"`
	ForcedEnd           uint64 `json:"hs_forced_end"`
	CadenceSwallowUntil uint64 `json:"hs_cadence_swallow_until"`
	SwallowFe           uint64 `json:"hs_swallow_fe"`
	TurnK               uint64 `json:"hs_turn_k"`
	TurnSlots           uint64 `json:"hs_turn_slots"`
	TurnReason          string `json:"hs_turn_reason,omitempty"`

	WarmKeys map[uint32]string `json:"warm_keys,omitempty"`
	HostStats []RootHostStat   `json:"host_stats,omitempty"`

	Inferences        []inferenceDiagEntry `json:"inferences,omitempty"`
	InferencesOmitted bool                 `json:"inferences_omitted,omitempty"`

	HostStatsHash  string `json:"host_stats_hash"`
	InferencesHash string `json:"inferences_hash"`
	WarmKeysHash   string `json:"warm_keys_hash"`
	HeightSyncHash string `json:"height_sync_hash"`
	RestHash       string `json:"rest_hash"`
	ComputedRoot   string `json:"computed_root"`
}

// RootHostStat is one slot's contribution to host_stats_hash.
type RootHostStat struct {
	Slot                 uint32 `json:"slot"`
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

// RootDivergenceError is the post_state_root mismatch plus the host inputs
// that produced the computed root. Error() keeps the existing phrase so
// callers that match on it still recognize the failure.
type RootDivergenceError struct {
	Inputs   RootInputs
	DiffRoot []byte
	Computed []byte
}

func (e *RootDivergenceError) Error() string {
	if e == nil {
		return types.ErrPostStateRootMismatch.Error()
	}
	return fmt.Sprintf("%s: diff %x, computed %x", types.ErrPostStateRootMismatch, e.DiffRoot, e.Computed)
}

func (e *RootDivergenceError) Unwrap() error { return types.ErrPostStateRootMismatch }

// AsRootDivergence returns the divergence carried by err, if any.
func AsRootDivergence(err error) *RootDivergenceError {
	var div *RootDivergenceError
	if errors.As(err, &div) {
		return div
	}
	return nil
}

// ExportRootInputs snapshots the current state as root inputs. nonce is the
// diff nonce the caller is comparing, not necessarily LatestNonce.
func (sm *StateMachine) ExportRootInputs(nonce uint64) RootInputs {
	if sm == nil {
		return RootInputs{Nonce: nonce}
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	root, err := sm.computeStateRootLocked()
	if err != nil {
		in := sm.rootInputsLocked(nonce, nil)
		return in
	}
	return sm.rootInputsLocked(nonce, root)
}

func (sm *StateMachine) rootInputsLocked(nonce uint64, computedRoot []byte) RootInputs {
	st := sm.state
	hs := types.HeightSyncEscrowCommitFromState(st)
	acc := sealedAccBytes32(st.SealedAcc)
	in := RootInputs{
		Nonce:               nonce,
		LatestNonce:         st.LatestNonce,
		Balance:             st.Balance,
		Fees:                st.Fees,
		Phase:               uint8(st.Phase),
		Version:             st.StateRootAndProtocolVersion,
		LiveInferences:      len(st.Inferences),
		SealedInferences:    len(sm.sealedNonces),
		SealedAcc:           hex.EncodeToString(acc[:]),
		ForcedStart:         hs.ForcedStart,
		ForcedEnd:           hs.ForcedEnd,
		CadenceSwallowUntil: hs.CadenceSwallowUntil,
		SwallowFe:           hs.SwallowFe,
		TurnK:               hs.TurnK,
		TurnSlots:           hs.TurnSlots,
		TurnReason:          hs.Reason,
		WarmKeys:            mapsClone(st.WarmKeys),
		HostStats:           rootHostStats(st.HostStats),
		ComputedRoot:        hex.EncodeToString(computedRoot),
	}
	if h, err := computeHostStatsHash(st.HostStats); err == nil {
		in.HostStatsHash = hex.EncodeToString(h)
	}
	if h, err := computeInferencesHash(st.Inferences); err == nil {
		in.InferencesHash = hex.EncodeToString(h)
	}
	in.WarmKeysHash = hex.EncodeToString(computeWarmKeysHash(st.WarmKeys))
	in.HeightSyncHash = hex.EncodeToString(hashHeightSyncEscrow(hs))
	if rest, err := ComputeRestHashV2(st.Balance, acc, st.Inferences, st.WarmKeys, hs); err == nil {
		in.RestHash = hex.EncodeToString(rest)
	}
	if len(st.Inferences)+len(sm.sealedNonces) <= rootInputInferenceCap {
		in.Inferences = sm.collectInferenceDiagEntriesLocked()
	} else {
		in.InferencesOmitted = true
	}
	return in
}

func mapsClone(src map[uint32]string) map[uint32]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[uint32]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func rootHostStats(src map[uint32]*types.HostStats) []RootHostStat {
	if len(src) == 0 {
		return nil
	}
	ids := make([]uint32, 0, len(src))
	for id := range src {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]RootHostStat, 0, len(ids))
	for _, id := range ids {
		s := src[id]
		if s == nil {
			continue
		}
		out = append(out, RootHostStat{
			Slot:                 id,
			Missed:               s.Missed,
			Invalid:              s.Invalid,
			Cost:                 s.Cost,
			RequiredValidations:  s.RequiredValidations,
			CompletedValidations: s.CompletedValidations,
		})
	}
	return out
}

// ParseHostRootInputs reads host_state from a divergence HTTP body.
func ParseHostRootInputs(body string) (RootInputs, bool) {
	var wrap struct {
		HostState *RootInputs `json:"host_state"`
	}
	if err := json.Unmarshal([]byte(body), &wrap); err != nil || wrap.HostState == nil {
		return RootInputs{}, false
	}
	return *wrap.HostState, true
}

// DiffRootInputs lists inputs that differ. local is the gateway, host is the
// value the host returned. Empty means every compared field matches.
func DiffRootInputs(local, host RootInputs) []string {
	var out []string
	cmp := func(name, a, b string) {
		if a != b {
			out = append(out, fmt.Sprintf("%s local=%s host=%s", name, a, b))
		}
	}
	cmpU := func(name string, a, b uint64) {
		if a != b {
			out = append(out, fmt.Sprintf("%s local=%d host=%d", name, a, b))
		}
	}
	cmpU("latest_nonce", local.LatestNonce, host.LatestNonce)
	cmpU("balance", local.Balance, host.Balance)
	cmpU("fees", local.Fees, host.Fees)
	cmpU("phase", uint64(local.Phase), uint64(host.Phase))
	cmp("version", local.Version, host.Version)
	cmpU("live_inferences", uint64(local.LiveInferences), uint64(host.LiveInferences))
	cmpU("sealed_inferences", uint64(local.SealedInferences), uint64(host.SealedInferences))
	cmp("sealed_acc", local.SealedAcc, host.SealedAcc)
	cmpU("hs_forced_start", local.ForcedStart, host.ForcedStart)
	cmpU("hs_forced_end", local.ForcedEnd, host.ForcedEnd)
	cmpU("hs_cadence_swallow_until", local.CadenceSwallowUntil, host.CadenceSwallowUntil)
	cmpU("hs_swallow_fe", local.SwallowFe, host.SwallowFe)
	cmpU("hs_turn_k", local.TurnK, host.TurnK)
	cmpU("hs_turn_slots", local.TurnSlots, host.TurnSlots)
	cmp("hs_turn_reason", local.TurnReason, host.TurnReason)
	cmp("warm_keys", formatWarmKeys(local.WarmKeys), formatWarmKeys(host.WarmKeys))
	cmp("host_stats", formatHostStats(local.HostStats), formatHostStats(host.HostStats))
	cmp("host_stats_hash", local.HostStatsHash, host.HostStatsHash)
	cmp("inferences_hash", local.InferencesHash, host.InferencesHash)
	cmp("warm_keys_hash", local.WarmKeysHash, host.WarmKeysHash)
	cmp("height_sync_hash", local.HeightSyncHash, host.HeightSyncHash)
	cmp("rest_hash", local.RestHash, host.RestHash)
	cmp("computed_root", local.ComputedRoot, host.ComputedRoot)
	return out
}

func formatWarmKeys(keys map[uint32]string) string {
	if len(keys) == 0 {
		return ""
	}
	ids := make([]uint32, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d=%s", id, keys[id]))
	}
	return strings.Join(parts, ",")
}

func formatHostStats(stats []RootHostStat) string {
	parts := make([]string, 0, len(stats))
	for _, s := range stats {
		parts = append(parts, fmt.Sprintf("%d:%d:%d:%d:%d:%d", s.Slot, s.Missed, s.Invalid, s.Cost, s.RequiredValidations, s.CompletedValidations))
	}
	return strings.Join(parts, ",")
}
