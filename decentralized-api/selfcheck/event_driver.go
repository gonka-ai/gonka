package selfcheck

import (
	"decentralized-api/broker"
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

// EventDriver advances a ChainPhaseTracker through a synthetic PoC
// cycle and triggers the broker commands that would normally be
// queued by the production OnNewBlockDispatcher.
//
// It does NOT simulate the full PoC artifact protocol — only the
// chain-side events the broker reacts to:
//   - epoch group data refresh (Inference phase boundary)
//   - PoC start trigger        (StartPocCommand)
//   - validation start         (InitValidateCommand)
//   - inference-up resume      (InferenceUpAllCommand)
//
// The Evaluator inspects broker state between transitions to judge
// whether the broker is behaving correctly. This is enough to catch
// onboarding regressions where the broker fails to react to common
// epoch events.
type EventDriver struct {
	Bridge       *MockChainBridge
	PhaseTracker *chainphase.ChainPhaseTracker
	Broker       *broker.Broker
	Epoch        *types.Epoch
	EpochParams  *types.EpochParams
}

// PushBlock updates the phase tracker to the given block height and
// returns the resulting EpochState. The hash is synthetic.
func (d *EventDriver) PushBlock(height int64) *chainphase.EpochState {
	d.PhaseTracker.Update(
		chainphase.BlockInfo{Height: height, Hash: "selfcheck-blk"},
		d.Epoch,
		d.EpochParams,
		true,
		nil,
	)
	return d.PhaseTracker.GetCurrentEpochState()
}

// RefreshEpochData updates the broker's per-node epoch view from the
// (mocked) chain. This mirrors what OnNewBlockDispatcher does at the
// start of every phase transition.
func (d *EventDriver) RefreshEpochData() error {
	es := d.PhaseTracker.GetCurrentEpochState()
	return d.Broker.UpdateNodeWithEpochData(es)
}

// TriggerStartPoC queues the StartPocCommand the broker would normally
// receive from OnNewBlockDispatcher when the chain crosses into the
// PoC generate phase.
func (d *EventDriver) TriggerStartPoC() error {
	return d.Broker.QueueMessage(broker.NewStartPocCommand())
}

// TriggerInitValidate queues the InitValidateCommand for the validation
// phase transition.
func (d *EventDriver) TriggerInitValidate() error {
	return d.Broker.QueueMessage(broker.NewInitValidateCommand())
}

// TriggerInferenceUpAll resumes inference at end of validation.
func (d *EventDriver) TriggerInferenceUpAll() error {
	return d.Broker.QueueMessage(broker.NewInferenceUpAllCommand())
}
