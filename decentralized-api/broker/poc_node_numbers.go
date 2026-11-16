package broker

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

const pocNodeNumbersKey = "poc_node_numbers"

var errPoCNodeNumberRolloutStage = errors.New("PoC node numbering rollout is waiting for the next stage")

// pocNodeNumberRollout makes re-enabling model-local numbering safe after a
// binary without the feature may have run. Ready is set immediately when the
// feature is first observed outside a generation window. If it is first
// observed during generation, new starts keep the global stride until the next
// stage. Dispatch is deferred only when a current-stage post2 mapping makes the
// already-running stride ambiguous.
type pocNodeNumberRollout struct {
	Ready        bool  `json:"ready"`
	LegacyHeight int64 `json:"legacy_height,omitempty"`
}

// Freeze nonce numbering for a stage; changing the stride can duplicate nonces.
// Store original node IDs so callbacks and weight distribution survive restarts.
// Rollout is optional for compatibility with the v0.2.16-post2 record.
type pocNodeNumbers struct {
	Height  int64                 `json:"height"`
	Models  map[string][]string   `json:"models"`
	Rollout *pocNodeNumberRollout `json:"rollout,omitempty"`
}

func (b *Broker) loadPoCNodeNumbers(ctx context.Context) error {
	if b.pocNodeNumbers != nil || b.configManager == nil || b.configManager.SqlDb() == nil {
		return nil
	}
	var numbers pocNodeNumbers
	found, err := apiconfig.KVGetJSON(ctx, b.configManager.SqlDb().GetDb(), pocNodeNumbersKey, &numbers)
	if err != nil {
		return err
	}
	if found {
		b.pocNodeNumbers = &numbers
	}
	return nil
}

func (b *Broker) savePoCNodeNumbers(ctx context.Context, numbers *pocNodeNumbers) error {
	if b.configManager != nil && b.configManager.SqlDb() != nil {
		if err := apiconfig.KVSetJSON(ctx, b.configManager.SqlDb().GetDb(), pocNodeNumbersKey, numbers); err != nil {
			return err
		}
	}
	b.pocNodeNumbers = numbers
	return nil
}

func activePoCGenerationHeight(epochState *chainphase.EpochState) (int64, bool) {
	if epochState == nil || epochState.IsNilOrNotSynced() {
		return 0, false
	}
	if challenge := overlayGeneratingChallengeWork(epochState); challenge != nil {
		return challenge.StartHeight(), true
	}
	switch epochState.CurrentPhase {
	case types.PoCGeneratePhase:
		return epochState.LatestEpoch.PocStartBlockHeight, true
	case types.PoCGenerateWindDownPhase:
		if epochState.LatestEpoch.IsPoCExchangeWindow(epochState.CurrentBlock.Height) {
			return epochState.LatestEpoch.PocStartBlockHeight, true
		}
	case types.InferencePhase:
		event := epochState.ActiveConfirmationPoCEvent
		if event != nil &&
			event.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION &&
			event.IsInBatchSubmissionWindow(epochState.CurrentBlock.Height, &epochState.LatestEpoch.EpochParams) {
			return event.TriggerHeight, true
		}
	}
	return 0, false
}

// preparePoCNodeNumberingRollout durably records whether this version first
// observed the node-numbering feature inside or outside a generation window.
// An old post2 mapping is retained so callbacks from already-running local-ID
// generators can still be attributed. A stale mapping does not activate local
// numbering in the observed generation stage.
func (b *Broker) preparePoCNodeNumberingRollout(ctx context.Context, epochState *chainphase.EpochState) error {
	b.pocNodeNumbersMu.Lock()
	defer b.pocNodeNumbersMu.Unlock()
	if err := b.loadPoCNodeNumbers(ctx); err != nil {
		return err
	}
	if b.pocNodeNumbers != nil && b.pocNodeNumbers.Rollout != nil {
		return nil
	}

	numbers := &pocNodeNumbers{}
	if b.pocNodeNumbers != nil {
		*numbers = *b.pocNodeNumbers
	}
	if height, active := activePoCGenerationHeight(epochState); active {
		numbers.Rollout = &pocNodeNumberRollout{LegacyHeight: height}
	} else {
		numbers.Rollout = &pocNodeNumberRollout{Ready: true}
	}
	return b.savePoCNodeNumbers(ctx, numbers)
}

func (b *Broker) assignPoCNodeNumber(ctx context.Context, nodeID string, params *pocParams, cmd *StartPoCNodeCommandV2) error {
	if params == nil {
		return errors.New("missing PoC parameters")
	}
	b.pocNodeNumbersMu.Lock()
	defer b.pocNodeNumbersMu.Unlock()
	if err := b.loadPoCNodeNumbers(ctx); err != nil {
		return err
	}

	// Defensive fallback for direct callers and startup errors: consider the
	// current stage legacy and persist that choice before dispatching anything.
	if b.pocNodeNumbers == nil || b.pocNodeNumbers.Rollout == nil {
		numbers := &pocNodeNumbers{}
		if b.pocNodeNumbers != nil {
			*numbers = *b.pocNodeNumbers
		}
		numbers.Rollout = &pocNodeNumberRollout{LegacyHeight: params.startPoCBlockHeight}
		if err := b.savePoCNodeNumbers(ctx, numbers); err != nil {
			return err
		}
	}

	if !b.pocNodeNumbers.Rollout.Ready &&
		b.pocNodeNumbers.Rollout.LegacyHeight == params.startPoCBlockHeight {
		if b.pocNodeNumbers.Height == params.startPoCBlockHeight && b.pocNodeNumbers.Models != nil {
			return errPoCNodeNumberRolloutStage
		}
		// No current-stage local mapping exists, so continuing the global stride
		// cannot conflict with a generator started by post2.
		return nil
	}

	if b.pocNodeNumbers.Height != params.startPoCBlockHeight || b.pocNodeNumbers.Models == nil {
		numbers := &pocNodeNumbers{
			Height: params.startPoCBlockHeight,
			Models: make(map[string][]string),
			Rollout: &pocNodeNumberRollout{
				Ready:        true,
				LegacyHeight: b.pocNodeNumbers.Rollout.LegacyHeight,
			},
		}
		allowed := b.pocModelFilter(params)
		b.mu.RLock()
		for id, node := range b.nodes {
			model, ok := resolvePoCModelForNodeWithFilter(&node.State, node.Node.Models, params, allowed)
			if ok {
				numbers.Models[model.ModelId] = append(numbers.Models[model.ModelId], id)
			}
		}
		b.mu.RUnlock()
		for _, ids := range numbers.Models {
			slices.Sort(ids)
		}
		if err := b.savePoCNodeNumbers(ctx, numbers); err != nil {
			return err
		}
	}

	ids := b.pocNodeNumbers.Models[cmd.Model]
	index := slices.Index(ids, nodeID)
	if index < 0 {
		return fmt.Errorf("node %s has no PoC allocation for model %s at height %d", nodeID, cmd.Model, params.startPoCBlockHeight)
	}
	cmd.ModelNodeID = &index
	cmd.TotalNodes = len(ids)
	return nil
}

// PoCNodeID translates a model-local number back to the weight distribution ID.
func (b *Broker) PoCNodeID(ctx context.Context, height int64, model string, index int) (string, error) {
	b.pocNodeNumbersMu.Lock()
	defer b.pocNodeNumbersMu.Unlock()
	if err := b.loadPoCNodeNumbers(ctx); err != nil {
		return "", err
	}
	if b.pocNodeNumbers == nil || b.pocNodeNumbers.Height != height {
		return "", fmt.Errorf("no PoC node allocation at height %d", height)
	}
	ids := b.pocNodeNumbers.Models[model]
	if index < 0 || index >= len(ids) {
		return "", fmt.Errorf("unknown PoC node number %d for model %s", index, model)
	}
	return ids[index], nil
}
