package state

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"devshard/logging"
	"devshard/types"
)

// inferenceDiagEntry is one row in the state-root mismatch inference snapshot.
type inferenceDiagEntry struct {
	ID          uint64 `json:"id"`
	Status      uint8  `json:"status"`
	ConfirmedAt int64  `json:"confirmed_at"`
	Sealed      bool   `json:"sealed"`
	SealedNonce uint64 `json:"sealed_nonce,omitempty"`
}

// StateRootMismatchOpts carries optional fields for a mismatch diagnostic log line.
type StateRootMismatchOpts struct {
	Side            string
	Nonce           uint64
	DiffPostState   []byte
	ComputedState   []byte
	SealClock       StateClockWindow
}

// IsPostStateRootMismatchError reports whether err is a post_state_root mismatch,
// including HTTP-wrapped errors returned to devshardctl clients.
func IsPostStateRootMismatchError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, types.ErrPostStateRootMismatch) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, types.ErrPostStateRootMismatch.Error()) ||
		strings.Contains(msg, "post_state_root does not match computed state root")
}

func (sm *StateMachine) collectInferenceDiagEntriesLocked() []inferenceDiagEntry {
	ids := make([]uint64, 0, len(sm.state.Inferences)+len(sm.sealedNonces))
	seen := make(map[uint64]struct{}, len(sm.state.Inferences)+len(sm.sealedNonces))

	for id := range sm.state.Inferences {
		ids = append(ids, id)
		seen[id] = struct{}{}
	}
	for id := range sm.sealedNonces {
		if _, ok := seen[id]; ok {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)

	entries := make([]inferenceDiagEntry, 0, len(ids))
	for _, id := range ids {
		entry := inferenceDiagEntry{ID: id}
		if rec, live := sm.state.Inferences[id]; live {
			entry.Status = uint8(rec.Status)
			entry.ConfirmedAt = rec.ConfirmedAt
			entry.Sealed = false
		} else {
			entry.Sealed = true
			entry.SealedNonce = sm.sealedNonces[id]
			if rec, ok := sm.lookupSealedInferenceLocked(id); ok {
				entry.Status = uint8(rec.Status)
				entry.ConfirmedAt = rec.ConfirmedAt
			} else if rec, err := sm.hydrateCommittedInferenceLocked(id); err == nil && rec != nil {
				entry.Status = uint8(rec.Status)
				entry.ConfirmedAt = rec.ConfirmedAt
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// LogStateRootMismatchDiagnostic emits a comparable snapshot for fork forensics.
// Caller should invoke while the state machine still reflects the view being
// diagnosed (host: before applyCore rollback; user: after local compose/apply).
func (sm *StateMachine) LogStateRootMismatchDiagnostic(opts StateRootMismatchOpts) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	sm.logStateRootMismatchDiagnosticLocked(opts)
}

// logStateRootMismatchDiagnosticLocked is the LogStateRootMismatchDiagnostic body.
// Caller must hold sm.mu (read or write).
func (sm *StateMachine) logStateRootMismatchDiagnosticLocked(opts StateRootMismatchOpts) {
	entries := sm.collectInferenceDiagEntriesLocked()
	entriesJSON, err := json.Marshal(entries)
	if err != nil {
		entriesJSON = []byte(fmt.Sprintf("marshal error: %v", err))
	}

	diagArgs := []any{
		"subsystem", "state",
		"diagnostic", "state_root_mismatch",
		"side", opts.Side,
		"nonce", opts.Nonce,
		"latest_nonce", sm.state.LatestNonce,
		"escrow_id", sm.state.EscrowID,
		"balance", sm.state.Balance,
		"group_size", len(sm.state.Group),
		"host_stats_count", len(sm.state.HostStats),
		"live_inferences_count", len(sm.state.Inferences),
		"sealed_inferences_count", len(sm.sealedNonces),
		"phase", sm.state.Phase,
		"warm_keys_count", len(sm.state.WarmKeys),
		"config_token_price", sm.state.Config.TokenPrice,
		"config_fee_per_nonce", sm.state.Config.FeePerNonce,
		"config_vote_threshold", sm.state.Config.VoteThreshold,
		"config_validation_rate", sm.state.Config.ValidationRate,
		"config_inference_seal_grace_nonces", sm.state.Config.InferenceSealGraceNonces,
		"config_inference_seal_grace_seconds", sm.state.Config.InferenceSealGraceSeconds,
		"sealed_acc", hex.EncodeToString(sm.state.SealedAcc),
		"inference_entries", string(entriesJSON),
	}
	if opts.SealClock.Known {
		diagArgs = append(diagArgs,
			"auto_seal_state_clock", opts.SealClock.Clock,
			"auto_seal_window_min_confirmed_at", opts.SealClock.MinConfirmedAt,
			"auto_seal_window_max_confirmed_at", opts.SealClock.MaxConfirmedAt,
		)
	}
	if len(opts.DiffPostState) > 0 {
		diagArgs = append(diagArgs, "diff_post_state_root", hex.EncodeToString(opts.DiffPostState))
	}
	if len(opts.ComputedState) > 0 {
		diagArgs = append(diagArgs, "computed_state_root", hex.EncodeToString(opts.ComputedState))
	}
	logging.Error("state root mismatch diagnostic", diagArgs...)
}
