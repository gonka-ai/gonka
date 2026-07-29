package hostctl

import (
	"context"
	"fmt"
	"time"
)

const (
	phaseStarted               = "started"
	phaseRuntimeValidated      = "runtime_validated"
	phaseMembershipLoaded      = "membership_loaded"
	phaseRestartPolicyResolved = "restart_policy_resolved"
	phaseRouterDraining        = "router_draining"
	phaseRestartPolicyCaptured = "restart_policy_captured"
	phaseRestartDisabled       = "restart_disabled"
	phaseTermRequested         = "term_requested"
	phaseRouterStopping        = "router_stopping"
	phaseTermSent              = "term_sent"
	phaseHostStopped           = "host_stopped"
	phaseRouterOffline         = "router_offline"
	phaseRouterRemoved         = "router_removed"
	phaseRouterJoining         = "router_joining"
	phaseHostStarted           = "host_started"
	phaseHostReady             = "host_ready"
	phaseRouterActive          = "router_active"
	phaseComplete              = "complete"
	phaseCanceled              = "canceled"

	cancellationNotRequested    = "not_requested"
	cancellationRequested       = "requested"
	cancellationRestartRestored = "restart_restored"
	cancellationRouterActive    = "router_active"
	cancellationComplete        = "complete"
)

type workflowPhaseField uint8

const (
	operationPhaseField workflowPhaseField = iota
	cancellationPhaseField
)

type workflowHandler func(*Orchestrator, context.Context, *Journal) error

type workflowStateSpec struct {
	order   int
	next    string
	handler workflowHandler
}

type workflowDefinition struct {
	name       string
	phaseField workflowPhaseField
	initial    string
	terminal   string
	states     map[string]workflowStateSpec
}

var evacuationWorkflow = newStopWorkflow("evacuate", false)
var decommissionWorkflow = newStopWorkflow("decommission", true)

var addWorkflow = workflowDefinition{
	name:       "add",
	phaseField: operationPhaseField,
	initial:    phaseStarted,
	terminal:   phaseComplete,
	states: map[string]workflowStateSpec{
		phaseStarted: {
			order:   0,
			next:    phaseRuntimeValidated,
			handler: (*Orchestrator).validateRuntimeStep,
		},
		phaseRuntimeValidated: {
			order:   1,
			next:    phaseRestartPolicyResolved,
			handler: (*Orchestrator).resolveProvisionRestartPolicyStep,
		},
		phaseRestartPolicyResolved: {
			order:   2,
			next:    phaseRouterJoining,
			handler: (*Orchestrator).joinRouterStep,
		},
		phaseRouterJoining: {
			order:   3,
			next:    phaseHostStarted,
			handler: (*Orchestrator).startProvisionedHostStep,
		},
		phaseHostStarted: {
			order:   4,
			next:    phaseHostReady,
			handler: (*Orchestrator).waitForProvisionedHostReadyStep,
		},
		phaseHostReady: {
			order:   5,
			next:    phaseRouterActive,
			handler: (*Orchestrator).activateRouterHostStep,
		},
		phaseRouterActive: {
			order:   6,
			next:    phaseComplete,
			handler: (*Orchestrator).completeWorkflowStep,
		},
		phaseComplete: {
			order: 7,
		},
	},
}

var replaceWorkflow = workflowDefinition{
	name:       "replace",
	phaseField: operationPhaseField,
	initial:    phaseStarted,
	terminal:   phaseComplete,
	states: map[string]workflowStateSpec{
		phaseStarted: {
			order:   0,
			next:    phaseMembershipLoaded,
			handler: (*Orchestrator).loadMembershipStep,
		},
		phaseMembershipLoaded: {
			order:   1,
			next:    phaseRuntimeValidated,
			handler: (*Orchestrator).validateRuntimeStep,
		},
		phaseRuntimeValidated: {
			order:   2,
			next:    phaseRestartPolicyResolved,
			handler: (*Orchestrator).resolveProvisionRestartPolicyStep,
		},
		phaseRestartPolicyResolved: {
			order:   3,
			next:    phaseRouterJoining,
			handler: (*Orchestrator).joinRouterStep,
		},
		phaseRouterJoining: {
			order:   4,
			next:    phaseHostStarted,
			handler: (*Orchestrator).startProvisionedHostStep,
		},
		phaseHostStarted: {
			order:   5,
			next:    phaseHostReady,
			handler: (*Orchestrator).waitForProvisionedHostReadyStep,
		},
		phaseHostReady: {
			order:   6,
			next:    phaseRouterActive,
			handler: (*Orchestrator).activateRouterHostStep,
		},
		phaseRouterActive: {
			order:   7,
			next:    phaseComplete,
			handler: (*Orchestrator).completeWorkflowStep,
		},
		phaseComplete: {
			order: 8,
		},
	},
}

var cancellationWorkflow = workflowDefinition{
	name:       "cancel",
	phaseField: cancellationPhaseField,
	initial:    cancellationNotRequested,
	terminal:   cancellationComplete,
	states: map[string]workflowStateSpec{
		cancellationNotRequested: {
			order:   0,
			next:    cancellationRequested,
			handler: (*Orchestrator).requestCancellationStep,
		},
		cancellationRequested: {
			order:   1,
			next:    cancellationRestartRestored,
			handler: (*Orchestrator).restoreRestartPolicyStep,
		},
		cancellationRestartRestored: {
			order:   2,
			next:    cancellationRouterActive,
			handler: (*Orchestrator).reactivateRouterStep,
		},
		cancellationRouterActive: {
			order:   3,
			next:    cancellationComplete,
			handler: (*Orchestrator).completeCancellationStep,
		},
		cancellationComplete: {
			order: 4,
		},
	},
}

func newStopWorkflow(name string, remove bool) workflowDefinition {
	states := map[string]workflowStateSpec{
		phaseStarted: {
			order:   0,
			next:    phaseRuntimeValidated,
			handler: (*Orchestrator).validateStopRuntimeStep,
		},
		phaseRuntimeValidated: {
			order:   1,
			next:    phaseRouterDraining,
			handler: (*Orchestrator).drainRouterStep,
		},
		phaseRouterDraining: {
			order:   2,
			next:    phaseRestartPolicyCaptured,
			handler: (*Orchestrator).captureRestartPolicyStep,
		},
		phaseRestartPolicyCaptured: {
			order:   3,
			next:    phaseRestartDisabled,
			handler: (*Orchestrator).disableRestartPolicyStep,
		},
		phaseRestartDisabled: {
			order:   4,
			next:    phaseTermRequested,
			handler: (*Orchestrator).recordSignalIntentStep,
		},
		phaseTermRequested: {
			order:   5,
			next:    phaseRouterStopping,
			handler: (*Orchestrator).stopRouterStep,
		},
		phaseRouterStopping: {
			order:   6,
			next:    phaseTermSent,
			handler: (*Orchestrator).sendTerminationStep,
		},
		phaseTermSent: {
			order:   7,
			next:    phaseHostStopped,
			handler: (*Orchestrator).waitForHostStopStep,
		},
		phaseHostStopped: {
			order:   8,
			next:    phaseRouterOffline,
			handler: (*Orchestrator).markRouterOfflineStep,
		},
	}
	if remove {
		states[phaseRouterOffline] = workflowStateSpec{
			order:   9,
			next:    phaseRouterRemoved,
			handler: (*Orchestrator).removeRouterHostStep,
		}
		states[phaseRouterRemoved] = workflowStateSpec{
			order:   10,
			next:    phaseComplete,
			handler: (*Orchestrator).completeWorkflowStep,
		}
		states[phaseComplete] = workflowStateSpec{order: 11}
	} else {
		states[phaseRouterOffline] = workflowStateSpec{
			order:   9,
			next:    phaseComplete,
			handler: (*Orchestrator).completeWorkflowStep,
		}
		states[phaseComplete] = workflowStateSpec{order: 10}
	}
	return workflowDefinition{
		name:       name,
		phaseField: operationPhaseField,
		initial:    phaseStarted,
		terminal:   phaseComplete,
		states:     states,
	}
}

func workflowForMode(mode string) *workflowDefinition {
	switch mode {
	case "evacuate":
		return &evacuationWorkflow
	case "decommission":
		return &decommissionWorkflow
	case "add":
		return &addWorkflow
	case "replace":
		return &replaceWorkflow
	default:
		return nil
	}
}

func stopWorkflow(mode string) *workflowDefinition {
	switch mode {
	case "evacuate":
		return &evacuationWorkflow
	case "decommission":
		return &decommissionWorkflow
	default:
		return nil
	}
}

func provisionWorkflow(mode string) *workflowDefinition {
	switch mode {
	case "add":
		return &addWorkflow
	case "replace":
		return &replaceWorkflow
	default:
		return nil
	}
}

func (w workflowDefinition) current(journal *Journal) string {
	if w.phaseField == cancellationPhaseField {
		if journal.CancellationPhase == "" {
			return cancellationNotRequested
		}
		return journal.CancellationPhase
	}
	return journal.Phase
}

func (w workflowDefinition) hasState(state string) bool {
	_, ok := w.states[state]
	return ok
}

func (w workflowDefinition) before(current, target string) bool {
	currentSpec, currentOK := w.states[current]
	targetSpec, targetOK := w.states[target]
	return currentOK && targetOK && currentSpec.order < targetSpec.order
}

func (w workflowDefinition) reached(current, target string) bool {
	currentSpec, currentOK := w.states[current]
	targetSpec, targetOK := w.states[target]
	return currentOK && targetOK && currentSpec.order >= targetSpec.order
}

func (o *Orchestrator) runWorkflow(
	ctx context.Context,
	journal *Journal,
	workflow *workflowDefinition,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := workflow.current(journal)
		if current == workflow.terminal {
			return nil
		}
		spec, ok := workflow.states[current]
		if !ok || spec.next == "" {
			return fmt.Errorf(
				"%s workflow has no transition from %q",
				workflow.name,
				current,
			)
		}
		if spec.handler != nil {
			if err := spec.handler(o, ctx, journal); err != nil {
				return fmt.Errorf(
					"%s workflow transition %s -> %s: %w",
					workflow.name,
					current,
					spec.next,
					err,
				)
			}
		}
		if err := o.checkpointWorkflow(journal, workflow, spec.next); err != nil {
			return fmt.Errorf(
				"checkpoint %s workflow at %s: %w",
				workflow.name,
				spec.next,
				err,
			)
		}
	}
}

func (o *Orchestrator) checkpointWorkflow(
	journal *Journal,
	workflow *workflowDefinition,
	phase string,
) error {
	if workflow.phaseField == cancellationPhaseField {
		journal.CancellationPhase = phase
	} else {
		journal.Phase = phase
	}
	journal.UpdatedAt = time.Now().UTC()
	return o.writeJournal(*journal)
}
