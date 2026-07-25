package hostctl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"versiond-router/internal/router"
)

func (o *Orchestrator) completeWorkflowStep(
	_ context.Context,
	_ *Journal,
) error {
	return nil
}

func (o *Orchestrator) validateRuntimeStep(
	ctx context.Context,
	_ *Journal,
) error {
	if err := o.versiondRuntime.ValidateStopContract(ctx, o.config.KillGrace); err != nil {
		return fmt.Errorf("versiond runtime preflight: %w", err)
	}
	return nil
}

func (o *Orchestrator) drainRouterStep(
	ctx context.Context,
	journal *Journal,
) error {
	return o.routerTransition(
		ctx,
		journal,
		router.HostActive,
		router.HostDraining,
		stopTarget(journal.Mode),
	)
}

func (o *Orchestrator) captureRestartPolicyStep(
	ctx context.Context,
	journal *Journal,
) error {
	if o.config.VersiondRuntime != RuntimeDocker ||
		journal.PreviousRestartPolicy != "" {
		return nil
	}
	policy, err := o.dockerRestartPolicy(ctx)
	if err != nil {
		return err
	}
	journal.PreviousRestartPolicy = policy
	return nil
}

func (o *Orchestrator) disableRestartPolicyStep(
	ctx context.Context,
	_ *Journal,
) error {
	if o.config.VersiondRuntime != RuntimeDocker {
		return nil
	}
	return o.setDockerRestartPolicy(ctx, "no")
}

func (o *Orchestrator) recordSignalIntentStep(
	ctx context.Context,
	journal *Journal,
) error {
	if err := o.validateRuntimeStep(ctx, journal); err != nil {
		return err
	}
	_, err := o.versiondRunning(ctx)
	return err
}

func (o *Orchestrator) stopRouterStep(
	ctx context.Context,
	journal *Journal,
) error {
	return o.routerTransition(
		ctx,
		journal,
		router.HostDraining,
		router.HostStopping,
		stopTarget(journal.Mode),
	)
}

func (o *Orchestrator) sendTerminationStep(
	ctx context.Context,
	_ *Journal,
) error {
	if o.config.VersiondRuntime == RuntimeDocker {
		// Cancellation may restore this mutable setting before a failed stop
		// workflow is resumed, so assert it immediately before every signal.
		if err := o.setDockerRestartPolicy(ctx, "no"); err != nil {
			return err
		}
	}
	running, err := o.versiondRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	return o.signalVersiond(ctx, "TERM")
}

func (o *Orchestrator) waitForHostStopStep(
	ctx context.Context,
	_ *Journal,
) error {
	stopped, err := o.waitForStopped(ctx, o.config.KillGrace)
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}
	slog.Warn(
		"versiond kill grace expired; sending SIGKILL",
		"service", o.config.VersiondService,
	)
	if err := o.signalVersiond(ctx, "KILL"); err != nil {
		return err
	}
	stopped, err = o.waitForStopped(ctx, o.config.PollInterval*3)
	if err != nil {
		return err
	}
	if !stopped {
		return errors.New("versiond remains running after SIGKILL")
	}
	return nil
}

func (o *Orchestrator) markRouterOfflineStep(
	ctx context.Context,
	journal *Journal,
) error {
	return o.routerTransition(
		ctx,
		journal,
		router.HostStopping,
		router.HostOffline,
		stopTarget(journal.Mode),
	)
}

func (o *Orchestrator) removeRouterHostStep(
	ctx context.Context,
	journal *Journal,
) error {
	return o.routerTransition(
		ctx,
		journal,
		router.HostOffline,
		router.HostRemoved,
		router.HostRemoved,
	)
}

func (o *Orchestrator) loadMembershipStep(
	ctx context.Context,
	journal *Journal,
) error {
	if journal.MembershipID != "" {
		return nil
	}
	return o.loadRouterMembership(ctx, journal)
}

func (o *Orchestrator) resolveProvisionRestartPolicyStep(
	_ context.Context,
	journal *Journal,
) error {
	if o.config.VersiondRuntime != RuntimeDocker ||
		journal.PreviousRestartPolicy != "" {
		return nil
	}
	policy, err := o.provisionRestartPolicy(journal.Mode, journal.MembershipID)
	if err != nil {
		return err
	}
	journal.PreviousRestartPolicy = policy
	return nil
}

func (o *Orchestrator) joinRouterStep(
	ctx context.Context,
	journal *Journal,
) error {
	if journal.Mode == "add" {
		return o.routerAdd(ctx, journal)
	}
	return o.routerTransition(
		ctx,
		journal,
		router.HostOffline,
		router.HostJoining,
		router.HostActive,
	)
}

func (o *Orchestrator) startProvisionedHostStep(
	ctx context.Context,
	journal *Journal,
) error {
	if err := o.validateRuntimeStep(ctx, journal); err != nil {
		return err
	}
	return o.startVersiond(ctx, journal.PreviousRestartPolicy)
}

func (o *Orchestrator) waitForProvisionedHostReadyStep(
	ctx context.Context,
	_ *Journal,
) error {
	return o.waitForReady(ctx)
}

func (o *Orchestrator) activateRouterHostStep(
	ctx context.Context,
	journal *Journal,
) error {
	return o.routerTransition(
		ctx,
		journal,
		router.HostJoining,
		router.HostActive,
		router.HostActive,
	)
}

func (o *Orchestrator) requestCancellationStep(
	ctx context.Context,
	journal *Journal,
) error {
	workflow := stopWorkflow(journal.Mode)
	if workflow == nil || !workflow.before(journal.Phase, phaseTermRequested) {
		return fmt.Errorf(
			"cannot cancel %s at or after signal intent (%s); resume it instead",
			journal.Mode,
			journal.Phase,
		)
	}
	running, err := o.versiondRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		return stoppedHostCancellationError(journal.Mode)
	}
	return nil
}

func (o *Orchestrator) restoreRestartPolicyStep(
	ctx context.Context,
	journal *Journal,
) error {
	running, err := o.versiondRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		return stoppedHostCancellationError(journal.Mode)
	}
	if o.config.VersiondRuntime != RuntimeDocker ||
		journal.PreviousRestartPolicy == "" {
		return nil
	}
	if err := o.setDockerRestartPolicy(ctx, journal.PreviousRestartPolicy); err != nil {
		return fmt.Errorf("restore Docker restart policy: %w", err)
	}
	return nil
}

func (o *Orchestrator) reactivateRouterStep(
	ctx context.Context,
	journal *Journal,
) error {
	running, err := o.versiondRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		return stoppedHostCancellationError(journal.Mode)
	}
	return o.routerCancel(ctx, journal)
}

func (o *Orchestrator) completeCancellationStep(
	_ context.Context,
	journal *Journal,
) error {
	journal.Phase = phaseCanceled
	return nil
}

func stoppedHostCancellationError(mode string) error {
	return fmt.Errorf(
		"cannot reactivate a stopped versiond host; resume %s with the same operation ID",
		mode,
	)
}

// resumeStopAfterFailedCancellation converts an impossible rollback into a
// forward recovery. It is safe only before the router reactivation edge and
// only after the process has already stopped.
func (o *Orchestrator) resumeStopAfterFailedCancellation(
	ctx context.Context,
	journal *Journal,
) (bool, error) {
	if !cancellationWorkflow.before(
		journal.CancellationPhase,
		cancellationRouterActive,
	) {
		return false, nil
	}
	running, err := o.versiondRunning(ctx)
	if err != nil {
		return false, fmt.Errorf(
			"check versiond before abandoning cancellation: %w",
			err,
		)
	}
	if running {
		return false, nil
	}
	if o.config.VersiondRuntime == RuntimeDocker {
		if err := o.setDockerRestartPolicy(ctx, "no"); err != nil {
			return false, fmt.Errorf(
				"disable Docker restart policy before resuming %s: %w",
				journal.Mode,
				err,
			)
		}
	}

	previousPhase := journal.CancellationPhase
	previousUpdatedAt := journal.UpdatedAt
	journal.CancellationPhase = ""
	journal.UpdatedAt = time.Now().UTC()
	if err := o.writeJournal(*journal); err != nil {
		journal.CancellationPhase = previousPhase
		journal.UpdatedAt = previousUpdatedAt
		return false, fmt.Errorf(
			"checkpoint abandoned cancellation before resuming %s: %w",
			journal.Mode,
			err,
		)
	}
	slog.Warn(
		"cancellation cannot reactivate a stopped versiond; resuming stop workflow",
		"operation_id", journal.OperationID,
		"mode", journal.Mode,
		"cancellation_phase", previousPhase,
	)
	return true, nil
}
