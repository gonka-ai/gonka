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
	blocks := nextPoC - currentHeight
	if blocks < 0 {
		blocks = 0
	}
	seconds := int64(float64(blocks) * apiconfig.DefaultBlockTimeSeconds)
	return &TimingInfo{
		CurrentPhase:        string(es.CurrentPhase),
		BlocksUntilNextPoC:  blocks,
		SecondsUntilNextPoC: seconds,
		ShouldBeOnline:      shouldBeOnline(es.CurrentPhase, seconds),
	}
}

func shouldBeOnline(phase types.EpochPhase, secondsUntilNextPoC int64) bool {
	switch phase {
	case types.PoCGeneratePhase, types.PoCGenerateWindDownPhase,
		types.PoCValidatePhase, types.PoCValidateWindDownPhase:
		return true
	}
	return secondsUntilNextPoC <= apiconfig.OnlineAlertLeadSeconds
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
