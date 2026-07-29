package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
)

// TimingInfo is the JSON shape returned to admins for one node,
// describing where the chain sits in the PoC cycle.
type TimingInfo struct {
	CurrentPhase        string `json:"current_phase"`
	BlocksUntilNextPoC  int64  `json:"blocks_until_next_poc"`
	SecondsUntilNextPoC int64  `json:"seconds_until_next_poc"`
	ShouldBeOnline      bool   `json:"should_be_online"`
	// InPoCWindow is true whenever PoC work is happening right now — either a
	// regular PoC phase or an active confirmation PoC event. Unlike
	// ShouldBeOnline (which also covers the pre-PoC lead time), this is the
	// "work in progress" signal the test-safety gates key off.
	InPoCWindow bool `json:"in_poc_window"`
	// ConfirmationPoCActive reports whether the countdown/online guidance is
	// driven by a confirmation PoC event rather than the epoch schedule.
	ConfirmationPoCActive bool `json:"confirmation_poc_active,omitempty"`
}

// ComputeTiming derives TimingInfo from the current epoch state.
// Returns nil when the chain phase tracker has not yet synced (the
// admin handler then omits the timing block entirely rather than
// reporting zeros).
func ComputeTiming(es *chainphase.EpochState) *TimingInfo {
	if es.IsNilOrNotSynced() {
		return nil
	}
	currentHeight := es.CurrentBlock.Height
	// The imminent PoC is this epoch's PoC start while we are still before
	// it (pre-PoC inference phase); only once we're past it does the next
	// PoC become the following epoch's. Using NextPoCStart() unconditionally
	// would overcount by an epoch and miss the real online-alert window.
	nextPoC := es.LatestEpoch.StartOfPoC()
	if currentHeight >= nextPoC {
		nextPoC = es.LatestEpoch.NextPoCStart()
	}

	// A confirmation PoC is an out-of-band PoC round: its generation window can
	// start long before the epoch's own PoC. Ignoring it (the previous behavior)
	// would report hours of slack while the node is in — or minutes from — real
	// PoC work, which is exactly when a multi-minute test must not start.
	confirmationActive := false
	if ev := es.ActiveConfirmationPoCEvent; ev != nil {
		params := es.LatestEpoch.EpochParams
		if confirmationEnd := ev.GetValidationEnd(&params); currentHeight <= confirmationEnd {
			confirmationActive = true
			if ev.GenerationStartHeight > currentHeight && ev.GenerationStartHeight < nextPoC {
				nextPoC = ev.GenerationStartHeight
			}
		}
	}

	blocks := nextPoC - currentHeight
	if blocks < 0 {
		blocks = 0
	}
	seconds := int64(float64(blocks) * apiconfig.DefaultBlockTimeSeconds)
	inWindow := inPoCWindow(es, currentHeight)
	return &TimingInfo{
		CurrentPhase:          string(es.CurrentPhase),
		BlocksUntilNextPoC:    blocks,
		SecondsUntilNextPoC:   seconds,
		ShouldBeOnline:        inWindow || seconds <= apiconfig.OnlineAlertLeadSeconds,
		InPoCWindow:           inWindow,
		ConfirmationPoCActive: confirmationActive,
	}
}

// inPoCWindow reports whether PoC work is happening at currentHeight: either a
// regular PoC phase, or a confirmation PoC event inside its generation,
// exchange, or validation window.
func inPoCWindow(es *chainphase.EpochState, currentHeight int64) bool {
	switch es.CurrentPhase {
	case types.PoCGeneratePhase, types.PoCGenerateWindDownPhase,
		types.PoCValidatePhase, types.PoCValidateWindDownPhase:
		return true
	}
	if ev := es.ActiveConfirmationPoCEvent; ev != nil {
		params := es.LatestEpoch.EpochParams
		if ev.IsInGenerationWindow(currentHeight, &params) ||
			ev.IsInExchangeWindow(currentHeight, &params) ||
			ev.IsInValidationWindow(currentHeight, &params) {
			return true
		}
	}
	return false
}

// formatShortDuration renders a non-negative seconds count as a compact
// human string such as "2h 15m", "8m", or "45s". Zero or negative
// values render as "0s".
func formatShortDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	switch {
	case h > 0 && m > 0:
		return strconv.FormatInt(h, 10) + "h " + strconv.FormatInt(m, 10) + "m"
	case h > 0:
		return strconv.FormatInt(h, 10) + "h"
	case m > 0 && s > 0:
		return strconv.FormatInt(m, 10) + "m " + strconv.FormatInt(s, 10) + "s"
	case m > 0:
		return strconv.FormatInt(m, 10) + "m"
	default:
		return strconv.FormatInt(s, 10) + "s"
	}
}
