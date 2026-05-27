// Package selfcheck provides a standalone mode for the api binary
// that exercises the broker against a mocked chain to verify the
// PoC lifecycle wiring is intact, without touching production code
// paths. It is the implementation of the approach suggested by
// reviewers on PR #866: keep the broker untouched, drive it with
// synthetic events, and observe outcomes from a separate evaluator.
//
// See proposals/onboarding-clarity-v1/README.md for product context.
package selfcheck

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"fmt"
	"os"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

const (
	selfParticipantAddress = "selfcheck-participant"
	selfNodeId             = "selfcheck-node-1"
	selfModelId            = "selfcheck-model"
)

// Run executes the selfcheck and returns a Report. The Report's Pass
// field is the overall outcome. An error is only returned if setup
// itself failed (e.g. broker could not be constructed); per-stage
// assertion failures are surfaced via Report.Pass=false.
//
// Run blocks until completion (a few seconds at most) or until ctx is
// cancelled.
func Run(ctx context.Context) (Report, error) {
	// Disable the production "enforced model id" gate — selfcheck uses
	// a synthetic model id and otherwise registration would be
	// rejected. Tests in broker_test.go set this same env var.
	if os.Getenv("ENFORCED_MODEL_ID") == "" {
		os.Setenv("ENFORCED_MODEL_ID", "disabled")
	}

	bridge := &MockChainBridge{
		ParticipantAddress: selfParticipantAddress,
		ModelId:            selfModelId,
		NodeId:             selfNodeId,
		EpochIndex:         1,
	}

	phaseParams, err := bridge.GetParams()
	if err != nil {
		return Report{}, fmt.Errorf("mock GetParams: %w", err)
	}

	// Anchor the synthetic epoch so phase math is consistent.
	const pocStart int64 = 1000
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: pocStart}
	phaseTracker := &chainphase.ChainPhaseTracker{}
	phaseTracker.UpdateEpochParams(*phaseParams.Params.EpochParams)

	// Configure a single fake node in the in-memory config manager so
	// the broker reads it as a known node. We use a real
	// ConfigManager wired to a tmpfile so SetNodes round-trips cleanly.
	cm, cleanup, err := newInMemoryConfig()
	if err != nil {
		return Report{}, fmt.Errorf("newInMemoryConfig: %w", err)
	}
	defer cleanup()
	if err := cm.SetNodes([]apiconfig.InferenceNodeConfig{{
		Id:            selfNodeId,
		Host:          "127.0.0.1",
		InferencePort: 18080,
		PoCPort:       18080,
		MaxConcurrent: 1,
		Models:        map[string]apiconfig.ModelConfig{selfModelId: {}},
	}}); err != nil {
		return Report{}, fmt.Errorf("SetNodes: %w", err)
	}

	mockFactory := mlnodeclient.NewMockClientFactory()
	participantInfo := &staticParticipant{addr: selfParticipantAddress}
	nodeBroker := broker.NewBroker(
		bridge,
		phaseTracker,
		participantInfo,
		"http://selfcheck-callback",
		mockFactory,
		cm,
	)

	// Load the configured node into the broker.
	for _, n := range cm.GetNodes() {
		ch := nodeBroker.LoadNodeToBroker(&n)
		if ch != nil {
			<-ch
		}
	}

	driver := &EventDriver{
		Bridge:       bridge,
		PhaseTracker: phaseTracker,
		Broker:       nodeBroker,
		Epoch:        epoch,
		EpochParams:  phaseParams.Params.EpochParams,
	}
	driver.PushBlock(pocStart - 50) // Inference phase, well before PoC

	if err := driver.RefreshEpochData(); err != nil {
		return Report{}, fmt.Errorf("RefreshEpochData: %w", err)
	}

	// Give the broker's goroutines a beat to process queued commands.
	if err := waitOrCtx(ctx, 200*time.Millisecond); err != nil {
		return Report{}, err
	}

	// nodeSyncWorker runs on a 60s ticker; trigger immediately so
	// AssertHardwareDiffSubmitted has something to observe within
	// selfcheck's short window.
	_ = nodeBroker.QueueMessage(broker.NewSyncNodesCommand())

	ev := NewEvaluator(nodeBroker, bridge, selfNodeId)
	registered := ev.AssertNodeRegistered()
	epochPopulated := ev.AssertEpochModelsPopulated()
	hwSubmitted := ev.AssertHardwareDiffSubmitted()

	return ev.Combine(registered, epochPopulated, hwSubmitted), nil
}

func waitOrCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// staticParticipant is a CurrenParticipantInfo whose values never
// change — enough for selfcheck where the participant is hardcoded.
type staticParticipant struct{ addr string }

func (p *staticParticipant) GetAddress() string { return p.addr }
func (p *staticParticipant) GetPubKey() string  { return "selfcheck-pubkey" }
