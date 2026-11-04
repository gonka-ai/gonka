package loadtest

import (
	"context"
	"errors"

	"devshard/testenv/gatewayphase"
)

// Each step uses the runner's detached cleanup on failure or cancellation.
// The host runtime stays alive: only ML inference is interrupted.
func pocWindowSteps(event plannedEvent, mocks mockControl) (eventStep, eventStep) {
	apply := func(ctx context.Context) error {
		for _, host := range event.pocHosts {
			if err := mocks.DAPIChallenge(ctx, host, true); err != nil {
				return err
			}
		}
		for _, node := range event.pocNodes {
			if err := mocks.MLFault(ctx, node, map[string]any{"abort_active": true}); err != nil {
				return err
			}
		}
		step := gatewayphase.StepJSON{At: "0s", Phase: gatewayphase.PhasePoCGenerate,
			PreservedParticipants: event.pocPreserved, Models: event.pocModels}
		if event.Params["kind"] == "cpoc" {
			step.Phase = gatewayphase.PhaseInference
			step.ConfirmationPoCPhase = gatewayphase.ConfirmationPoCGeneration
		}
		return mocks.GatewayPhase(ctx, []gatewayphase.StepJSON{step})
	}
	revert := func(ctx context.Context) error {
		var errs []error
		for _, host := range event.pocHosts {
			errs = append(errs, mocks.DAPIChallenge(ctx, host, false))
		}
		errs = append(errs, mocks.GatewayPhase(ctx, nil))
		return errors.Join(errs...)
	}
	return apply, revert
}
