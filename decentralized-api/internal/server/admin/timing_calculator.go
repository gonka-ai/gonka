package admin

import (
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

type Window struct {
	Start int64
	End   int64
	Label string
}

type TimingResult struct {
	BlocksUntilNextPoC  int64
	SecondsUntilNextPoC int64
}

type Countdown struct {
	Phase          string
	NextPoCSeconds int64
	ShouldBeOnline bool
}

type TimingCalculator struct{}

func NewTimingCalculator() *TimingCalculator { return &TimingCalculator{} }

func (t *TimingCalculator) ComputePoCSchedule(ec *types.EpochContext) []Window {
	if ec == nil {
		return nil
	}
	return []Window{
		{Start: ec.StartOfPoC(), End: ec.PoCGenerationWindDown() - 1, Label: string(types.PoCGeneratePhase)},
		{Start: ec.PoCGenerationWindDown(), End: ec.StartOfPoCValidation() - 1, Label: string(types.PoCGenerateWindDownPhase)},
		{Start: ec.StartOfPoCValidation(), End: ec.PoCValidationWindDown() - 1, Label: string(types.PoCValidatePhase)},
		{Start: ec.PoCValidationWindDown(), End: ec.EndOfPoCValidation(), Label: string(types.PoCValidateWindDownPhase)},
		{Start: ec.EndOfPoCValidation() + 1, End: ec.NextPoCStart() - 1, Label: string(types.InferencePhase)},
	}
}

func (t *TimingCalculator) TimeUntilNextPoC(es *chainphase.EpochState, blockTimeSeconds float64) TimingResult {
	if es == nil || !es.IsSynced {
		return TimingResult{}
	}
	ec := es.LatestEpoch
	current := es.CurrentBlock.Height
	next := ec.NextPoCStart()
	blocks := next - current
	if blocks < 0 {
		blocks = 0
	}
	seconds := int64(float64(blocks) * blockTimeSeconds)
	return TimingResult{BlocksUntilNextPoC: blocks, SecondsUntilNextPoC: seconds}
}

func (t *TimingCalculator) SafeOffline(es *chainphase.EpochState, blockTimeSeconds float64, minSeconds int64) bool {
	if es == nil || !es.IsSynced {
		return false
	}
	phase := es.CurrentPhase
	if phase == types.PoCGeneratePhase || phase == types.PoCGenerateWindDownPhase || phase == types.PoCValidatePhase || phase == types.PoCValidateWindDownPhase {
		return false
	}
	tr := t.TimeUntilNextPoC(es, blockTimeSeconds)
	return tr.SecondsUntilNextPoC > minSeconds
}

func (t *TimingCalculator) ComputeSafeOfflineWindows(es *chainphase.EpochState, blockTimeSeconds float64, minSeconds int64) []Window {
	if es == nil || !es.IsSynced {
		return nil
	}
	if es.CurrentPhase != types.InferencePhase {
		return []Window{}
	}
	ec := es.LatestEpoch
	start := es.CurrentBlock.Height
	end := ec.NextPoCStart() - 1
	if end < start {
		return []Window{}
	}
	seconds := int64(float64(end-start) * blockTimeSeconds)
	if seconds <= minSeconds {
		return []Window{}
	}
	return []Window{{Start: start, End: end, Label: "OfflineSafe"}}
}

func (t *TimingCalculator) OnlineAlert(es *chainphase.EpochState, blockTimeSeconds float64, leadSeconds int64) bool {
	if es == nil || !es.IsSynced {
		return false
	}
	phase := es.CurrentPhase
	if phase == types.PoCGeneratePhase || phase == types.PoCGenerateWindDownPhase || phase == types.PoCValidatePhase || phase == types.PoCValidateWindDownPhase {
		return true
	}
	tr := t.TimeUntilNextPoC(es, blockTimeSeconds)
	return tr.SecondsUntilNextPoC <= leadSeconds
}

func (t *TimingCalculator) Countdown(es *chainphase.EpochState, blockTimeSeconds float64, leadSeconds int64) Countdown {
	if es == nil || !es.IsSynced {
		return Countdown{}
	}
	tr := t.TimeUntilNextPoC(es, blockTimeSeconds)
	should := t.OnlineAlert(es, blockTimeSeconds, leadSeconds)
	return Countdown{Phase: string(es.CurrentPhase), NextPoCSeconds: tr.SecondsUntilNextPoC, ShouldBeOnline: should}
}
