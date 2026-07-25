package hostctl

import (
	"context"
	"fmt"
)

// validateStopRuntime requires an external kill contract only while there is
// still a running service to stop. A stopped or absent service has already
// reached the runtime side of the stop workflow.
func (o *Orchestrator) validateStopRuntime(
	ctx context.Context,
) (serviceState, error) {
	state, err := o.versiondServiceState(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect versiond runtime state: %w", err)
	}
	if state != serviceRunning {
		return state, nil
	}
	return o.validateRunningStopContract(ctx)
}

func (o *Orchestrator) validateRunningStopContract(
	ctx context.Context,
) (serviceState, error) {
	contractErr := o.versiondRuntime.ValidateStopContract(
		ctx,
		o.config.KillGrace,
	)
	if contractErr == nil {
		return serviceRunning, nil
	}
	rechecked, stateErr := o.versiondServiceState(ctx)
	if stateErr == nil && rechecked != serviceRunning {
		return rechecked, nil
	}
	if stateErr != nil {
		return "", fmt.Errorf(
			"versiond runtime preflight: %v; recheck service state: %w",
			contractErr,
			stateErr,
		)
	}
	return serviceRunning, fmt.Errorf(
		"versiond runtime preflight: %w",
		contractErr,
	)
}

func (o *Orchestrator) captureDockerRestartPolicyIfPresent(
	ctx context.Context,
) (string, error) {
	state, err := o.versiondServiceState(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Docker service before reading restart policy: %w", err)
	}
	if state == serviceAbsent {
		return "", nil
	}

	policy, policyErr := o.dockerRestartPolicy(ctx)
	if policyErr == nil {
		return policy, nil
	}
	rechecked, stateErr := o.versiondServiceState(ctx)
	if stateErr == nil && rechecked == serviceAbsent {
		return "", nil
	}
	if stateErr != nil {
		return "", fmt.Errorf(
			"read Docker restart policy: %v; recheck service state: %w",
			policyErr,
			stateErr,
		)
	}
	return "", fmt.Errorf("read Docker restart policy: %w", policyErr)
}

// reconcileDockerRestartDisabled applies restart=no when a Docker container
// still exists and then returns the freshly observed service state. If the
// container disappears during docker update, absence is the desired stop
// state and the workflow can continue.
func (o *Orchestrator) reconcileDockerRestartDisabled(
	ctx context.Context,
	observed serviceState,
) (serviceState, error) {
	if o.config.VersiondRuntime != RuntimeDocker ||
		observed == serviceAbsent {
		return observed, nil
	}

	updateErr := o.setDockerRestartPolicy(ctx, "no")
	current, stateErr := o.versiondServiceState(ctx)
	if updateErr != nil {
		if stateErr == nil && current == serviceAbsent {
			return serviceAbsent, nil
		}
		if stateErr != nil {
			return "", fmt.Errorf(
				"disable Docker restart policy: %v; recheck service state: %w",
				updateErr,
				stateErr,
			)
		}
		return current, fmt.Errorf("disable Docker restart policy: %w", updateErr)
	}
	if stateErr != nil {
		return "", fmt.Errorf(
			"recheck Docker service after disabling restart policy: %w",
			stateErr,
		)
	}
	return current, nil
}

func (o *Orchestrator) terminateVersiondIfRunning(
	ctx context.Context,
) error {
	state, err := o.versiondServiceState(ctx)
	if err != nil {
		return fmt.Errorf("inspect versiond before SIGTERM: %w", err)
	}
	state, err = o.reconcileDockerRestartDisabled(ctx, state)
	if err != nil {
		return err
	}
	if state != serviceRunning {
		return nil
	}
	state, err = o.validateRunningStopContract(ctx)
	if err != nil {
		return err
	}
	if state != serviceRunning {
		return nil
	}

	signalErr := o.signalVersiond(ctx, "TERM")
	if signalErr == nil {
		return nil
	}
	rechecked, stateErr := o.versiondServiceState(ctx)
	if stateErr == nil && rechecked != serviceRunning {
		return nil
	}
	if stateErr != nil {
		return fmt.Errorf(
			"send SIGTERM to versiond: %v; recheck service state: %w",
			signalErr,
			stateErr,
		)
	}
	return fmt.Errorf("send SIGTERM to versiond: %w", signalErr)
}
