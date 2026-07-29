package hostctl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkflowTablesAreCompleteAndForwardOnly(t *testing.T) {
	workflows := []*workflowDefinition{
		&evacuationWorkflow,
		&decommissionWorkflow,
		&addWorkflow,
		&replaceWorkflow,
		&cancellationWorkflow,
	}

	for _, workflow := range workflows {
		t.Run(workflow.name, func(t *testing.T) {
			if !workflow.hasState(workflow.initial) {
				t.Fatalf("initial state %q is not defined", workflow.initial)
			}
			terminal, ok := workflow.states[workflow.terminal]
			if !ok {
				t.Fatalf("terminal state %q is not defined", workflow.terminal)
			}
			if terminal.next != "" || terminal.handler != nil {
				t.Fatalf("terminal state %q has an outgoing transition", workflow.terminal)
			}

			for state, spec := range workflow.states {
				if state == workflow.terminal {
					continue
				}
				if spec.next == "" {
					t.Errorf("non-terminal state %q has no target", state)
					continue
				}
				if spec.handler == nil {
					t.Errorf("non-terminal state %q has no handler", state)
				}
				target, ok := workflow.states[spec.next]
				if !ok {
					t.Errorf("state %q targets unknown state %q", state, spec.next)
					continue
				}
				if target.order <= spec.order {
					t.Errorf(
						"state %q order %d does not advance to %q order %d",
						state,
						spec.order,
						spec.next,
						target.order,
					)
				}
			}

			visited := make(map[string]struct{}, len(workflow.states))
			for state := workflow.initial; state != workflow.terminal; {
				if _, ok := visited[state]; ok {
					t.Fatalf("canonical path contains a cycle at %q", state)
				}
				visited[state] = struct{}{}
				spec, ok := workflow.states[state]
				if !ok || spec.next == "" {
					t.Fatalf("canonical path stops at %q", state)
				}
				state = spec.next
			}
		})
	}
}

func TestWorkflowExecutorResumesAfterLastSuccessfulCheckpoint(t *testing.T) {
	orchestrator := newTestOrchestrator(t, &fakeRemote{}, "workflow-resume")
	journal := Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   "workflow-resume",
		Mode:          "test",
		Scope:         orchestrator.operationScope(),
		Phase:         phaseStarted,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	var firstRuns, secondRuns int
	failSecond := true
	workflow := workflowDefinition{
		name:       "test",
		phaseField: operationPhaseField,
		initial:    phaseStarted,
		terminal:   phaseComplete,
		states: map[string]workflowStateSpec{
			phaseStarted: {
				order: 0,
				next:  "middle",
				handler: func(
					_ *Orchestrator,
					_ context.Context,
					_ *Journal,
				) error {
					firstRuns++
					return nil
				},
			},
			"middle": {
				order: 1,
				next:  phaseComplete,
				handler: func(
					_ *Orchestrator,
					_ context.Context,
					_ *Journal,
				) error {
					secondRuns++
					if failSecond {
						return errors.New("injected transition failure")
					}
					return nil
				},
			},
			phaseComplete: {order: 2},
		},
	}

	err := orchestrator.runWorkflow(context.Background(), &journal, &workflow)
	if err == nil || !strings.Contains(err.Error(), "injected transition failure") {
		t.Fatalf("workflow error = %v, want injected transition failure", err)
	}
	if journal.Phase != "middle" {
		t.Fatalf("phase after failed edge = %q, want middle", journal.Phase)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "middle")

	failSecond = false
	if err := orchestrator.runWorkflow(context.Background(), &journal, &workflow); err != nil {
		t.Fatal(err)
	}
	if journal.Phase != phaseComplete {
		t.Fatalf("resumed workflow phase = %q, want complete", journal.Phase)
	}
	if firstRuns != 1 || secondRuns != 2 {
		t.Fatalf(
			"handler runs = first:%d second:%d, want first:1 second:2",
			firstRuns,
			secondRuns,
		)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseComplete)
}
