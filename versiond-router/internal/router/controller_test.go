package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu                   sync.Mutex
	calls                []string
	failTestOnce         bool
	failReloadOnce       bool
	outputPath           string
	rejectConfigContains []byte
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	if r.failTestOnce && len(args) == 1 && args[0] == "-t" {
		r.failTestOnce = false
		return errors.New("injected config validation failure")
	}
	if len(r.rejectConfigContains) > 0 &&
		len(args) == 1 && args[0] == "-t" {
		config, err := os.ReadFile(r.outputPath)
		if err != nil {
			return fmt.Errorf("read config under validation: %w", err)
		}
		if bytes.Contains(config, r.rejectConfigContains) {
			return errors.New("injected persistent config validation failure")
		}
	}
	if r.failReloadOnce && len(args) == 2 && args[0] == "-s" && args[1] == "reload" {
		r.failReloadOnce = false
		return errors.New("injected reload failure")
	}
	return nil
}

func TestControllerBootstrapAndDrainTransaction(t *testing.T) {
	controller, runner, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(state)); err != nil {
		t.Fatal(err)
	}

	updated, err := controller.Transition(context.Background(), Transition{
		OperationID: "drain-2", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Hosts[updated.hostIndex("versiond-2")].State != HostDraining {
		t.Fatal("versiond-2 was not persisted as draining")
	}
	config, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(config, []byte(
		"server versiond-2:8080 resolve max_fails=1 fail_timeout=10s down;",
	)) {
		t.Fatalf("draining host was not rendered down:\n%s", config)
	}
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operation journal should be removed, stat error = %v", err)
	}
	runner.mu.Lock()
	calls := strings.Join(runner.calls, "\n")
	runner.mu.Unlock()
	if !strings.Contains(calls, "nginx -t") || !strings.Contains(calls, "nginx -s reload") {
		t.Fatalf("nginx validation/reload calls missing:\n%s", calls)
	}
}

func TestControllerValidationFailureRestoresOutputButKeepsDesiredState(
	t *testing.T,
) {
	controller, runner, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	oldConfig, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.failTestOnce = true
	runner.mu.Unlock()

	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "invalid-rendered-drain", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	}); err == nil {
		t.Fatal("expected injected nginx validation failure")
	}
	assertFileEquals(t, paths.OutputPath, oldConfig)
	desired, err := controller.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if desired.Hosts[desired.hostIndex("versiond-2")].State != HostDraining {
		t.Fatalf("desired state was rolled back: %#v", desired.Hosts)
	}
	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Application.Converged {
		t.Fatalf("rejected config reported converged: %#v", status.Application)
	}

	if _, err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Application.Converged {
		t.Fatalf("router did not converge after validation retry: %#v", status.Application)
	}
}

func TestControllerRecoveryRefreshesRejectedConfigAfterTemplateFix(
	t *testing.T,
) {
	controller, runner, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	brokenMarker := []byte("broken_config_directive")
	brokenTemplate := []byte(testTemplate + "\nbroken_config_directive;\n")
	if err := os.WriteFile(paths.TemplatePath, brokenTemplate, 0o600); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.rejectConfigContains = brokenMarker
	runner.mu.Unlock()

	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "repair-rendered-drain", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	}); err == nil {
		t.Fatal("expected persistent nginx validation failure")
	}
	journal, err := controller.loadOperationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if journal == nil || journal.RenderRevision != 1 ||
		journal.RenderSourceSHA != renderSourceSHA(
			brokenTemplate,
			DefaultProxyPolicy(),
		) {
		t.Fatalf("initial config projection = %#v", journal)
	}
	firstEventID := journal.Audit.EventID

	if _, err := controller.Recover(context.Background()); err == nil {
		t.Fatal("unchanged broken template unexpectedly recovered")
	}
	journal, err = controller.loadOperationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if journal.RenderRevision != 1 || journal.Audit.EventID != firstEventID {
		t.Fatalf("unchanged source created a new projection: %#v", journal)
	}

	fixedTemplate := []byte(testTemplate + "\n# corrected projection\n")
	if err := os.WriteFile(paths.TemplatePath, fixedTemplate, 0o600); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.failReloadOnce = true
	runner.mu.Unlock()
	if _, err := controller.Recover(context.Background()); err == nil {
		t.Fatal("expected injected reload failure after projection refresh")
	}
	journal, err = controller.loadOperationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if journal.RenderRevision != 2 {
		t.Fatalf("refreshed render revision = %d, want 2", journal.RenderRevision)
	}
	if journal.RenderSourceSHA != renderSourceSHA(
		fixedTemplate,
		DefaultProxyPolicy(),
	) {
		t.Fatalf("refreshed render source = %s", journal.RenderSourceSHA)
	}
	if journal.Audit.EventID == firstEventID {
		t.Fatal("refreshed projection retained the old audit event id")
	}
	if !bytes.Contains(journal.NewConfig, []byte("# corrected projection")) {
		t.Fatalf("refreshed config does not use fixed template:\n%s", journal.NewConfig)
	}

	if _, err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	want, err := Render(fixedTemplate, journal.NewState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, paths.OutputPath, want)
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered journal remains: %v", err)
	}
}

func TestControllerReloadFailureLeavesDesiredStateForRecovery(t *testing.T) {
	controller, runner, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(state)); err != nil {
		t.Fatal(err)
	}
	oldApplied, err := controller.loadAppliedState()
	if err != nil {
		t.Fatal(err)
	}

	runner.mu.Lock()
	runner.failReloadOnce = true
	runner.mu.Unlock()
	_, err = controller.Transition(context.Background(), Transition{
		OperationID: "failed-drain", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if err == nil {
		t.Fatal("expected injected reload failure")
	}
	desired, err := controller.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if desired.Generation != state.Generation+1 ||
		desired.Hosts[desired.hostIndex("versiond-2")].State != HostDraining {
		t.Fatalf("desired state was not committed: %#v", desired)
	}
	applied, err := controller.loadAppliedState()
	if err != nil {
		t.Fatal(err)
	}
	if *applied != *oldApplied {
		t.Fatalf("applied state changed after failed reload: %#v", applied)
	}
	journal, err := controller.loadOperationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if journal == nil || journal.Phase != operationPhaseDesiredPersisted {
		t.Fatalf("pending journal = %#v", journal)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != desired.Generation {
		t.Fatalf("recovered generation = %d, want %d", recovered.Generation, desired.Generation)
	}
	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Application.Converged {
		t.Fatalf("router did not converge after retry: %#v", status.Application)
	}
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered journal remains, stat error = %v", err)
	}
}

func TestControllerRemoveDropsHostFromRenderedPool(t *testing.T) {
	controller, _, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(state)); err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]HostState{
		{HostActive, HostDraining},
		{HostDraining, HostStopping},
		{HostStopping, HostOffline},
		{HostOffline, HostRemoved},
	} {
		if _, err := controller.Transition(context.Background(), Transition{
			OperationID: "decommission-2", Host: "versiond-2",
			From: edge[0], To: edge[1], Target: HostRemoved,
		}); err != nil {
			t.Fatalf("%s -> %s: %v", edge[0], edge[1], err)
		}
	}

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.hostIndex("versiond-2") >= 0 {
		t.Fatal("removed host remains in router state")
	}
	if status.ActiveTransfer != nil {
		t.Fatalf("completed removal retained transfer %#v", status.ActiveTransfer)
	}
	config, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(config, []byte("versiond-2:8080")) {
		t.Fatalf("removed host remains in nginx config:\n%s", config)
	}

	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "decommission-2", Host: "versiond-2",
		From: HostOffline, To: HostRemoved, Target: HostRemoved,
	}); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
}

func TestControllerRemoveReloadFailureKeepsTerminalDesiredState(t *testing.T) {
	controller, runner, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(state)); err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]HostState{
		{HostActive, HostDraining},
		{HostDraining, HostStopping},
		{HostStopping, HostOffline},
	} {
		if _, err := controller.Transition(context.Background(), Transition{
			OperationID: "decommission-2", Host: "versiond-2",
			From: edge[0], To: edge[1], Target: HostRemoved,
		}); err != nil {
			t.Fatalf("%s -> %s: %v", edge[0], edge[1], err)
		}
	}
	runner.mu.Lock()
	runner.failReloadOnce = true
	runner.mu.Unlock()
	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "decommission-2", Host: "versiond-2",
		From: HostOffline, To: HostRemoved, Target: HostRemoved,
	}); err == nil {
		t.Fatal("expected injected reload failure")
	}
	journalData, err := os.ReadFile(paths.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journalData, []byte(`"new_receipts"`)) {
		t.Fatalf("schema-5 WAL contains the full receipt index:\n%s", journalData)
	}
	if !bytes.Contains(journalData, []byte(`"completion_receipt"`)) {
		t.Fatalf("schema-5 WAL has no operation receipt:\n%s", journalData)
	}
	desired, err := controller.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if desired.hostIndex("versiond-2") >= 0 {
		t.Fatalf("desired state retained removed host: %#v", desired.Hosts)
	}
	receipts, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, completed := receipts.Completed["decommission-2"]; !completed {
		t.Fatal("terminal desired state has no completion receipt")
	}
	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "decommission-2", Host: "versiond-2",
		From: HostOffline, To: HostRemoved, Target: HostRemoved,
	}); err != nil {
		t.Fatalf("retry removal after reconcile failure: %v", err)
	}
	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Application.Converged {
		t.Fatalf("router did not converge after retry: %#v", status.Application)
	}
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered journal remains, stat error = %v", err)
	}
}

func TestControllerCompletedOperationDoesNotDependOnAudit(t *testing.T) {
	controller, runner, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(state)); err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]HostState{
		{HostActive, HostDraining},
		{HostDraining, HostStopping},
		{HostStopping, HostOffline},
	} {
		if _, err := controller.Transition(context.Background(), Transition{
			OperationID: "evacuate-2", Host: "versiond-2",
			From: edge[0], To: edge[1], Target: HostOffline,
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner.mu.Lock()
	callCount := len(runner.calls)
	runner.mu.Unlock()
	if err := os.WriteFile(
		paths.AuditPath,
		[]byte("{this is not valid json\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	got, err := controller.Transition(context.Background(), Transition{
		OperationID: "evacuate-2", Host: "versiond-2",
		From: HostStopping, To: HostOffline, Target: HostOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Hosts[got.hostIndex("versiond-2")].State != HostOffline {
		t.Fatalf("completed retry changed host state: %#v", got.Hosts)
	}
	runner.mu.Lock()
	callsAfterRetry := len(runner.calls)
	runner.mu.Unlock()
	if callsAfterRetry != callCount {
		t.Fatalf("completed retry ran %d extra nginx commands", callsAfterRetry-callCount)
	}

	_, err = controller.Transition(context.Background(), Transition{
		OperationID: "evacuate-2", Host: "versiond-1",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if !errors.Is(err, ErrOperationOwner) {
		t.Fatalf("reused operation error = %v, want ErrOperationOwner", err)
	}
}

func TestControllerAuditFailureDoesNotRollbackAppliedGeneration(t *testing.T) {
	controller, _, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.AuditPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.AuditPath, 0o700); err != nil {
		t.Fatal(err)
	}

	updated, err := controller.Transition(context.Background(), Transition{
		OperationID: "drain-with-audit-down", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != updated.Generation || !status.Application.Converged {
		t.Fatalf("routing commit depends on audit: %#v", status)
	}
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audit failure retained operation journal: %v", err)
	}
	outbox, err := controller.loadAuditOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox.Pending) != 1 {
		t.Fatalf("pending audit events = %d, want 1", len(outbox.Pending))
	}

	if err := os.Remove(paths.AuditPath); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	outbox, err = controller.loadAuditOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox.Pending) != 0 {
		t.Fatalf("delivered audit events remain pending: %#v", outbox.Pending)
	}
	audit, err := os.ReadFile(paths.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(audit, []byte(`"operation_id":"drain-with-audit-down"`)) {
		t.Fatalf("delivered audit record is missing:\n%s", audit)
	}
}

func TestControllerNoopCancelDoesNotConsumeOperationID(t *testing.T) {
	controller, _, _ := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Cancel(context.Background(), CancelTransfer{
		OperationID: "future-cancel",
		Host:        "versiond-2",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "future-cancel", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	}); err != nil {
		t.Fatalf("noop cancel consumed operation id: %v", err)
	}
	if _, err := controller.Cancel(context.Background(), CancelTransfer{
		OperationID: "future-cancel",
		Host:        "versiond-2",
	}); err != nil {
		t.Fatalf("cancel active transfer: %v", err)
	}
}

func TestControllerCompletedAddEdgeIsReplayable(t *testing.T) {
	controller, runner, _ := newTestController(t)
	state, err := NewState(
		[]string{"versiond-1"},
		8080,
		"versiond-1",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	joining, err := controller.Add(context.Background(), AddMembership{
		OperationID: "add-2",
		Host:        "versiond-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	membershipID := membershipOf(t, joining, "versiond-2")
	change := Transition{
		OperationID: "add-2", MembershipID: membershipID,
		Host: "versiond-2", From: HostJoining, To: HostActive,
		Target: HostActive,
	}
	active, err := controller.Transition(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	callsBeforeReplay := len(runner.calls)
	runner.mu.Unlock()

	replayed, err := controller.Transition(context.Background(), change)
	if err != nil {
		t.Fatalf("replay completed add edge: %v", err)
	}
	if replayed.Generation != active.Generation {
		t.Fatalf(
			"completed add replay changed generation from %d to %d",
			active.Generation,
			replayed.Generation,
		)
	}
	runner.mu.Lock()
	callsAfterReplay := len(runner.calls)
	runner.mu.Unlock()
	if callsAfterReplay != callsBeforeReplay {
		t.Fatalf(
			"completed add replay ran %d nginx commands",
			callsAfterReplay-callsBeforeReplay,
		)
	}
	otherEdge := change
	otherEdge.From = HostActive
	otherEdge.To = HostDraining
	if _, err := controller.Transition(
		context.Background(),
		otherEdge,
	); !errors.Is(err, ErrOperationOwner) {
		t.Fatalf(
			"unrelated edge error = %v, want ErrOperationOwner",
			err,
		)
	}
}

func TestControllerReAddCreatesNewMembershipWithoutRevivingRemovedOne(t *testing.T) {
	controller, _, _ := newTestController(t)
	state := newTestState(t)
	oldMembership := membershipOf(t, state, "versiond-2")
	if _, err := controller.Bootstrap(context.Background(), staticState(state)); err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]HostState{
		{HostActive, HostDraining},
		{HostDraining, HostStopping},
		{HostStopping, HostOffline},
		{HostOffline, HostRemoved},
	} {
		if _, err := controller.Transition(context.Background(), Transition{
			OperationID: "decommission-2", MembershipID: oldMembership,
			Host: "versiond-2", From: edge[0], To: edge[1],
			Target: HostRemoved,
		}); err != nil {
			t.Fatal(err)
		}
	}
	added, err := controller.Add(context.Background(), AddMembership{
		OperationID: "add-2",
		Host:        "versiond-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	newMembership := membershipOf(t, added, "versiond-2")
	if newMembership == oldMembership {
		t.Fatal("new host membership reused the removed membership id")
	}
	active, err := controller.Transition(context.Background(), Transition{
		OperationID: "add-2", MembershipID: newMembership,
		Host: "versiond-2", From: HostJoining, To: HostActive,
		Target: HostActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Hosts[active.hostIndex("versiond-2")].State != HostActive {
		t.Fatalf("re-added membership is not active: %#v", active.Hosts)
	}
	if _, err := controller.Add(context.Background(), AddMembership{
		OperationID:  "add-2",
		MembershipID: newMembership,
		Host:         "versiond-2",
	}); err != nil {
		t.Fatalf("idempotent completed add: %v", err)
	}

	retried, err := controller.Transition(context.Background(), Transition{
		OperationID: "decommission-2", MembershipID: oldMembership,
		Host: "versiond-2", From: HostOffline, To: HostRemoved,
		Target: HostRemoved,
	})
	if err != nil {
		t.Fatal(err)
	}
	if membershipOf(t, retried, "versiond-2") != newMembership {
		t.Fatal("retry of old decommission affected the new membership")
	}
}

func TestControllerStatusReportsCommittedIntentWithoutSideEffects(t *testing.T) {
	controller, runner, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(oldState)); err != nil {
		t.Fatal(err)
	}
	newState, oldConfig, newConfig := stagePendingDrain(
		t,
		paths,
		oldState,
		operationPhaseIntentCommitted,
	)
	runner.mu.Lock()
	callsBefore := len(runner.calls)
	runner.mu.Unlock()

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != newState.Generation {
		t.Fatalf("desired generation = %d, want %d", status.Generation, newState.Generation)
	}
	if status.PendingOperation == nil ||
		status.PendingOperation.Phase != operationPhaseIntentCommitted {
		t.Fatalf(
			"pending operation = %#v, want intent_committed phase",
			status.PendingOperation,
		)
	}
	if status.PendingOperation.MembershipID == "" ||
		status.PendingOperation.From != HostActive ||
		status.PendingOperation.To != HostDraining ||
		status.PendingOperation.Target != HostOffline {
		t.Fatalf("pending FSM edge = %#v", status.PendingOperation)
	}
	assertFileEquals(t, paths.OutputPath, oldConfig)
	runner.mu.Lock()
	callsAfter := len(runner.calls)
	runner.mu.Unlock()
	if callsAfter != callsBefore {
		t.Fatalf("status executed %d command(s)", callsAfter-callsBefore)
	}
	if status.Application.Converged {
		t.Fatalf("pending desired state reported converged: %#v", status.Application)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != newState.Generation {
		t.Fatalf("recovered generation = %d, want %d", recovered.Generation, newState.Generation)
	}
	assertFileEquals(t, paths.OutputPath, newConfig)
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-forward journal should be removed, stat error = %v", err)
	}
}

func TestControllerStatusDoesNotPersistLegacyJournalMigration(t *testing.T) {
	controller, runner, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	journal, newState, _, _ := newLegacyV2DrainJournal(
		t,
		controller,
		paths,
		oldState,
		"reloaded",
	)
	if err := writeJSONAtomic(paths.JournalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	callsBefore := len(runner.calls)
	runner.mu.Unlock()

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != newState.Generation ||
		status.PendingOperation == nil ||
		status.PendingOperation.Phase != operationPhaseIntentCommitted {
		t.Fatalf("legacy pending status = %#v", status)
	}
	assertFileEquals(t, paths.JournalPath, before)
	runner.mu.Lock()
	callsAfter := len(runner.calls)
	runner.mu.Unlock()
	if callsAfter != callsBefore {
		t.Fatalf("status executed %d command(s)", callsAfter-callsBefore)
	}
}

func TestControllerRecoveryRollsForwardCommittedIntent(t *testing.T) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(oldState)); err != nil {
		t.Fatal(err)
	}
	newState, _, newConfig := stagePendingDrain(
		t,
		paths,
		oldState,
		operationPhaseIntentCommitted,
	)

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != newState.Generation {
		t.Fatalf("recovered generation = %d, want %d", recovered.Generation, newState.Generation)
	}
	assertFileEquals(t, paths.OutputPath, newConfig)
}

func TestControllerRecoveryDoesNotReloadAppliedGeneration(t *testing.T) {
	controller, runner, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	stagePendingDrain(t, paths, oldState, operationPhaseApplied)
	runner.mu.Lock()
	reloadsBefore := countRunnerCalls(runner.calls, "nginx -s reload")
	runner.mu.Unlock()

	if _, err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	reloadsAfter := countRunnerCalls(runner.calls, "nginx -s reload")
	runner.mu.Unlock()
	if reloadsAfter != reloadsBefore {
		t.Fatalf(
			"recovery reloaded an applied generation %d time(s)",
			reloadsAfter-reloadsBefore,
		)
	}
}

func TestControllerTemplateChangeAfterAppliedUsesSeparateReconcile(
	t *testing.T,
) {
	controller, runner, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	newState, _, _ := stagePendingDrain(
		t,
		paths,
		oldState,
		operationPhaseApplied,
	)
	fixedTemplate := []byte(testTemplate + "\n# post-apply template\n")
	if err := os.WriteFile(paths.TemplatePath, fixedTemplate, 0o600); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	reloadsBefore := countRunnerCalls(runner.calls, "nginx -s reload")
	runner.mu.Unlock()

	if _, err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	reloadsAfter := countRunnerCalls(runner.calls, "nginx -s reload")
	runner.mu.Unlock()
	if reloadsAfter != reloadsBefore+1 {
		t.Fatalf(
			"template reconciliation reloaded %d time(s), want 1",
			reloadsAfter-reloadsBefore,
		)
	}
	applied, err := controller.loadAppliedState()
	if err != nil {
		t.Fatal(err)
	}
	if applied == nil || applied.OperationID != "bootstrap-render" {
		t.Fatalf("post-apply reconciliation metadata = %#v", applied)
	}
	want, err := Render(fixedTemplate, newState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, paths.OutputPath, want)
}

func TestControllerRecoveryRollsForwardTerminalStateAndReceipt(t *testing.T) {
	controller, _, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]HostState{
		{HostActive, HostDraining},
		{HostDraining, HostStopping},
		{HostStopping, HostOffline},
	} {
		var err error
		state, err = controller.Transition(context.Background(), Transition{
			OperationID: "decommission-2", Host: "versiond-2",
			From: edge[0], To: edge[1], Target: HostRemoved,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	oldState := state
	newState := oldState.Clone()
	outcome, err := newState.Advance(Transition{
		OperationID: "decommission-2", Host: "versiond-2",
		From: HostOffline, To: HostRemoved, Target: HostRemoved,
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	newConfig, err := Render(template, newState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(paths.OutputPath, newConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	change := mutation{
		OperationID:  "decommission-2",
		Action:       "transfer",
		Host:         "versiond-2",
		MembershipID: outcome.MembershipID,
		From:         HostOffline,
		To:           HostRemoved,
		Target:       HostRemoved,
		Result:       "completed",
	}
	completionReceipt := receiptFromMutation(change)
	if err := writeJSONAtomic(paths.JournalPath, operationJournal{
		SchemaVersion:  operationJournalSchemaVersion,
		OperationID:    change.OperationID,
		Phase:          operationPhaseIntentCommitted,
		Action:         change.Action,
		Host:           change.Host,
		MembershipID:   change.MembershipID,
		From:           change.From,
		To:             change.To,
		Target:         change.Target,
		Result:         change.Result,
		NewState:       newState,
		Receipt:        &completionReceipt,
		NewConfig:      newConfig,
		NewConfigSHA:   hashBytes(newConfig),
		RenderRevision: 1,
		RenderSourceSHA: renderSourceSHA(
			template,
			DefaultProxyPolicy(),
		),
		Audit:          auditRecord(change, newState.Generation, hashBytes(newConfig)),
		RecoveryPolicy: recoveryPolicyRollForward,
		Reload:         true,
		CreatedAt:      time.Now().UTC(),
	}, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.hostIndex("versiond-2") >= 0 {
		t.Fatalf("rolled-forward host remains in state: %#v", recovered.Hosts)
	}
	receipts, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := receipts.Completed["decommission-2"]; !ok {
		t.Fatal("rolled-forward terminal transition has no receipt")
	}
	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "decommission-2", MembershipID: outcome.MembershipID,
		Host: "versiond-2", From: HostOffline, To: HostRemoved,
		Target: HostRemoved,
	}); err != nil {
		t.Fatalf("replay after roll-forward: %v", err)
	}
}

func TestControllerRecoveryRepublishesChangedOutputConfig(t *testing.T) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(oldState)); err != nil {
		t.Fatal(err)
	}
	_, _, newConfig := stagePendingDrain(
		t,
		paths,
		oldState,
		operationPhaseDesiredPersisted,
	)
	if err := writeFileAtomic(paths.OutputPath, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, paths.OutputPath, newConfig)
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered journal remains: %v", err)
	}
}

func TestControllerBootstrapBuildsInitialStateOnlyWhenMissing(t *testing.T) {
	controller, _, _ := newTestController(t)
	want := newTestState(t)
	builds := 0

	got, err := controller.Bootstrap(context.Background(), func() (State, error) {
		builds++
		return want, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("initial state factory called %d times, want 1", builds)
	}
	if got.Generation != want.Generation || got.LastOperation != want.LastOperation {
		t.Fatalf("bootstrap state = %#v, want %#v", got, want)
	}

	got, err = controller.Bootstrap(context.Background(), func() (State, error) {
		builds++
		return State{}, errors.New("bootstrap environment is unavailable")
	})
	if err != nil {
		t.Fatalf("bootstrap with persisted state: %v", err)
	}
	if builds != 1 {
		t.Fatalf("factory called after state was persisted: %d calls", builds)
	}
	if got.Generation != want.Generation || got.LastOperation != want.LastOperation {
		t.Fatalf("persisted state = %#v, want %#v", got, want)
	}
}

func TestControllerBootstrapImportsReceiptsBeforeAuditRotation(t *testing.T) {
	controller, _, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.ReceiptsPath); err != nil {
		t.Fatal(err)
	}
	if err := controller.appendAudit(AuditRecord{
		Time:         time.Now().UTC(),
		OperationID:  "completed-before-upgrade",
		Action:       "activate",
		Host:         "versiond-2",
		MembershipID: membershipOf(t, state, "versiond-2"),
		To:           HostActive,
		Target:       HostActive,
		Result:       "completed",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Bootstrap(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AuditPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.completedOperation("completed-before-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.Host != "versiond-2" {
		t.Fatalf("bootstrap did not persist imported receipt: %#v", receipt)
	}
}

func TestControllerBootstrapMigratesLegacyRouterState(t *testing.T) {
	controller, _, paths := newTestController(t)
	updatedAt := time.Now().Add(-time.Hour).UTC()
	legacy := stateV1{
		SchemaVersion: legacySchemaVersion,
		Generation:    7,
		Port:          8080,
		LegacyHost:    "versiond-1",
		Hosts: []hostV1{
			{
				Name:    "versiond-1",
				Address: "versiond-1",
				State:   HostActive,
			},
			{
				Name:        "versiond-2",
				Address:     "versiond-2",
				State:       HostDraining,
				OperationID: "legacy-evacuation",
			},
		},
		LastOperation: "legacy-evacuation",
		UpdatedAt:     updatedAt,
	}
	if err := writeJSONAtomic(paths.StatePath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := controller.Bootstrap(
		context.Background(),
		func() (State, error) {
			return State{}, errors.New("initial-state factory must not run")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %d, want %d", state.SchemaVersion, SchemaVersion)
	}
	for _, host := range state.Hosts {
		if !strings.HasPrefix(host.MembershipID, "membership-migrated-") {
			t.Fatalf("host %s membership = %q", host.Name, host.MembershipID)
		}
	}
	if state.ActiveTransfer == nil ||
		state.ActiveTransfer.ID != "legacy-evacuation" ||
		state.ActiveTransfer.To != HostOffline ||
		!state.ActiveTransfer.Migrated {
		t.Fatalf("migrated transfer = %#v", state.ActiveTransfer)
	}
	if _, err := controller.Transition(context.Background(), Transition{
		OperationID: "legacy-evacuation", Host: "versiond-2",
		From: HostDraining, To: HostStopping, Target: HostOffline,
	}); err != nil {
		t.Fatalf("resume migrated transfer: %v", err)
	}
	persisted, err := os.ReadFile(paths.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope stateEnvelope
	if err := json.Unmarshal(persisted, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != SchemaVersion {
		t.Fatalf("persisted schema = %d", envelope.SchemaVersion)
	}
}

func TestControllerStatusDoesNotPersistRouterStateMigration(t *testing.T) {
	controller, _, paths := newTestController(t)
	legacy := stateV1{
		SchemaVersion: legacySchemaVersion,
		Generation:    7,
		Port:          8080,
		LegacyHost:    "versiond-1",
		Hosts: []hostV1{
			{
				Name:    "versiond-1",
				Address: "versiond-1",
				State:   HostActive,
			},
		},
		LastOperation: "legacy-bootstrap",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := writeJSONAtomic(paths.StatePath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State.SchemaVersion != SchemaVersion {
		t.Fatalf(
			"status schema = %d, want migrated schema %d",
			status.State.SchemaVersion,
			SchemaVersion,
		)
	}
	data, err := os.ReadFile(paths.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope stateEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != legacySchemaVersion {
		t.Fatalf(
			"read-only status persisted schema %d",
			envelope.SchemaVersion,
		)
	}
}

func TestControllerBootstrapMigratesIntermediateMembershipState(t *testing.T) {
	controller, _, paths := newTestController(t)
	legacy := newTestState(t)
	membershipID := membershipOf(t, legacy, "versiond-2")
	if _, err := legacy.Advance(Transition{
		OperationID: "intermediate-evacuation", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	}); err != nil {
		t.Fatal(err)
	}
	legacy.SchemaVersion = legacySchemaVersion
	if err := writeJSONAtomic(paths.StatePath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := controller.Bootstrap(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := membershipOf(t, state, "versiond-2"); got != membershipID {
		t.Fatalf("membership changed from %s to %s", membershipID, got)
	}
	if state.ActiveTransfer == nil ||
		state.ActiveTransfer.ID != "intermediate-evacuation" ||
		state.ActiveTransfer.Migrated {
		t.Fatalf("intermediate transfer migration = %#v", state.ActiveTransfer)
	}
}

func TestControllerRecoveryMigratesSchemaV2PreReloadByRollback(t *testing.T) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	journal, _, oldConfig, newConfig := newLegacyV2DrainJournal(
		t,
		controller,
		paths,
		oldState,
		"config_published",
	)
	if err := writeFileAtomic(paths.OutputPath, newConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(paths.JournalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != oldState.Generation {
		t.Fatalf(
			"legacy pre-reload generation = %d, want %d",
			recovered.Generation,
			oldState.Generation,
		)
	}
	assertFileEquals(t, paths.OutputPath, oldConfig)
}

func TestControllerRecoveryMigratesSchemaV2ReloadedByConvergingForward(
	t *testing.T,
) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	journal, newState, _, newConfig := newLegacyV2DrainJournal(
		t,
		controller,
		paths,
		oldState,
		"reloaded",
	)
	if err := writeFileAtomic(paths.OutputPath, []byte("partially rolled back"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(paths.JournalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != newState.Generation {
		t.Fatalf(
			"legacy reloaded generation = %d, want %d",
			recovered.Generation,
			newState.Generation,
		)
	}
	assertFileEquals(t, paths.OutputPath, newConfig)
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated schema-2 journal remains: %v", err)
	}
}

func TestControllerRecoveryMigratesSchemaV3Projection(t *testing.T) {
	controller, runner, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	newState, _, newConfig := stagePendingDrain(
		t,
		paths,
		oldState,
		operationPhaseIntentCommitted,
	)
	current, err := controller.loadOperationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if current == nil {
		t.Fatal("staged operation journal is missing")
	}
	legacy := operationJournalV3FromCurrent(*current)
	if err := writeJSONAtomic(paths.JournalPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.PendingOperation == nil ||
		status.PendingOperation.RenderRevision != 1 {
		t.Fatalf("schema-3 pending status = %#v", status.PendingOperation)
	}
	var envelope operationJournalEnvelope
	data, err := os.ReadFile(paths.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != fixedConfigOperationJournalSchemaVersion {
		t.Fatalf("read-only status persisted schema %d", envelope.SchemaVersion)
	}

	runner.mu.Lock()
	runner.failReloadOnce = true
	runner.mu.Unlock()
	if _, err := controller.Recover(context.Background()); err == nil {
		t.Fatal("expected injected reload failure after schema-3 migration")
	}
	migrated, err := controller.loadOperationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if migrated == nil ||
		migrated.SchemaVersion != operationJournalSchemaVersion ||
		migrated.RenderRevision != 2 {
		t.Fatalf("migrated config projection = %#v", migrated)
	}
	template, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.RenderSourceSHA != renderSourceSHA(
		template,
		DefaultProxyPolicy(),
	) {
		t.Fatalf("migrated render source = %s", migrated.RenderSourceSHA)
	}
	if !bytes.Equal(migrated.NewConfig, newConfig) {
		t.Fatalf("migrated config changed:\n%s", migrated.NewConfig)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != newState.Generation {
		t.Fatalf(
			"recovered generation = %d, want %d",
			recovered.Generation,
			newState.Generation,
		)
	}
}

func TestControllerRecoveryMigratesSchemaV4ReceiptSnapshot(t *testing.T) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	newState, _, _ := stagePendingDrain(
		t,
		paths,
		oldState,
		operationPhaseIntentCommitted,
	)
	current, err := controller.loadOperationJournal()
	if err != nil {
		t.Fatal(err)
	}
	if current == nil {
		t.Fatal("staged operation journal is missing")
	}
	legacy := operationJournalV4FromCurrent(*current)
	if err := writeJSONAtomic(paths.JournalPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.PendingOperation == nil {
		t.Fatal("schema-4 journal is not visible in status")
	}
	data, err := os.ReadFile(paths.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope operationJournalEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != receiptSnapshotJournalSchemaVersion {
		t.Fatalf("read-only status persisted schema %d", envelope.SchemaVersion)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != newState.Generation {
		t.Fatalf(
			"recovered generation = %d, want %d",
			recovered.Generation,
			newState.Generation,
		)
	}
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated schema-4 journal remains: %v", err)
	}
}

func TestControllerRecoveryMigratesPendingJournalSchemaV1(t *testing.T) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(oldState),
	); err != nil {
		t.Fatal(err)
	}
	oldConfig, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	newState := oldState.Clone()
	result, err := newState.Advance(Transition{
		OperationID: "legacy-journal", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	newConfig, err := Render(template, newState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(paths.OutputPath, newConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	oldState.SchemaVersion = legacySchemaVersion
	newState.SchemaVersion = legacySchemaVersion
	oldStateJSON, err := json.Marshal(oldState)
	if err != nil {
		t.Fatal(err)
	}
	newStateJSON, err := json.Marshal(newState)
	if err != nil {
		t.Fatal(err)
	}
	legacyJournal := operationJournalV1{
		SchemaVersion: legacyOperationJournalSchemaVersion,
		OperationID:   "legacy-journal",
		Phase:         "reloaded",
		Action:        "transfer",
		Host:          "versiond-2",
		MembershipID:  result.MembershipID,
		From:          HostActive,
		To:            HostDraining,
		Target:        HostOffline,
		Result:        "advanced",
		OldState:      oldStateJSON,
		NewState:      newStateJSON,
		OldConfig:     oldConfig,
		NewConfigSHA:  hashBytes(newConfig),
		Reload:        true,
		CreatedAt:     time.Now().UTC(),
	}
	if err := writeJSONAtomic(paths.JournalPath, legacyJournal, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SchemaVersion != SchemaVersion {
		t.Fatalf("recovered schema = %d", recovered.SchemaVersion)
	}
	if recovered.Hosts[recovered.hostIndex("versiond-2")].State != HostDraining {
		t.Fatalf("recovered hosts = %#v", recovered.Hosts)
	}
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated journal remains: %v", err)
	}
}

func TestMigratedAddJournalReceiptReplaysFinalEdge(t *testing.T) {
	controller, _, _ := newTestController(t)
	oldState, err := NewState(
		[]string{"versiond-1"},
		8080,
		"versiond-1",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	added, err := oldState.Add(AddMembership{
		OperationID: "legacy-add",
		Host:        "versiond-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	change := Transition{
		OperationID:  "legacy-add",
		MembershipID: added.MembershipID,
		Host:         "versiond-2",
		From:         HostJoining,
		To:           HostActive,
		Target:       HostActive,
	}
	newState := oldState.Clone()
	if _, err := newState.Advance(change); err != nil {
		t.Fatal(err)
	}
	oldState.SchemaVersion = legacySchemaVersion
	newState.SchemaVersion = legacySchemaVersion
	oldStateJSON, err := json.Marshal(oldState)
	if err != nil {
		t.Fatal(err)
	}
	newStateJSON, err := json.Marshal(newState)
	if err != nil {
		t.Fatal(err)
	}
	legacyJournalJSON, err := json.Marshal(operationJournalV1{
		SchemaVersion: legacyOperationJournalSchemaVersion,
		OperationID:   change.OperationID,
		Phase:         "reloaded",
		Action:        "transfer",
		Host:          change.Host,
		MembershipID:  change.MembershipID,
		From:          change.From,
		To:            change.To,
		Target:        change.Target,
		Result:        "completed",
		OldState:      oldStateJSON,
		NewState:      newStateJSON,
		Reload:        true,
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	migrated, err := controller.migrateOperationJournalV1(
		legacyJournalJSON,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := migrated.NewReceipts.Completed[change.OperationID]
	if !ok {
		t.Fatal("migrated add journal has no completion receipt")
	}
	if receipt.Action != "add" {
		t.Fatalf("migrated receipt action = %q, want add", receipt.Action)
	}
	if err := matchCompletedTransition(receipt, change); err != nil {
		t.Fatalf("replay migrated add edge: %v", err)
	}
}

func TestControllerRecoveryMigratesPreFSMJournalSchemaV1(t *testing.T) {
	controller, _, paths := newTestController(t)
	updatedAt := time.Now().Add(-time.Hour).UTC()
	oldStateV1 := stateV1{
		SchemaVersion: legacySchemaVersion,
		Generation:    9,
		Port:          8080,
		LegacyHost:    "versiond-1",
		Hosts: []hostV1{
			{Name: "versiond-1", Address: "versiond-1", State: HostActive},
			{Name: "versiond-2", Address: "versiond-2", State: HostActive},
		},
		LastOperation: "previous-operation",
		UpdatedAt:     updatedAt,
	}
	newStateV1 := oldStateV1
	newStateV1.Generation++
	newStateV1.LastOperation = "legacy-drain"
	newStateV1.UpdatedAt = updatedAt.Add(time.Minute)
	newStateV1.Hosts = append([]hostV1(nil), oldStateV1.Hosts...)
	newStateV1.Hosts[1].State = HostDraining
	newStateV1.Hosts[1].OperationID = "legacy-drain"

	oldStateJSON, err := json.Marshal(oldStateV1)
	if err != nil {
		t.Fatal(err)
	}
	newStateJSON, err := json.Marshal(newStateV1)
	if err != nil {
		t.Fatal(err)
	}
	oldState, err := migrateStateV1(oldStateJSON)
	if err != nil {
		t.Fatal(err)
	}
	newState, err := migrateStateV1(newStateJSON)
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	oldConfig, err := Render(template, oldState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	newConfig, err := Render(template, newState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(paths.StatePath, oldStateV1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(paths.OutputPath, newConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(paths.JournalPath, operationJournalV1{
		SchemaVersion: legacyOperationJournalSchemaVersion,
		OperationID:   "legacy-drain",
		Phase:         "reloaded",
		Action:        "drain",
		Host:          "versiond-2",
		From:          HostActive,
		OldState:      oldStateJSON,
		NewState:      newStateJSON,
		OldConfig:     oldConfig,
		NewConfigSHA:  hashBytes(newConfig),
		Reload:        true,
		CreatedAt:     updatedAt,
	}, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	index := recovered.hostIndex("versiond-2")
	if index < 0 || recovered.Hosts[index].State != HostDraining {
		t.Fatalf("recovered hosts = %#v", recovered.Hosts)
	}
	if recovered.ActiveTransfer == nil ||
		recovered.ActiveTransfer.ID != "legacy-drain" ||
		recovered.ActiveTransfer.To != HostOffline ||
		!recovered.ActiveTransfer.Migrated {
		t.Fatalf("recovered transfer = %#v", recovered.ActiveTransfer)
	}
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated journal remains: %v", err)
	}
}

func newLegacyV2DrainJournal(
	t *testing.T,
	controller *Controller,
	paths Config,
	oldState State,
	phase string,
) (operationJournalV2, State, []byte, []byte) {
	t.Helper()
	oldConfig, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	newState := oldState.Clone()
	result, err := newState.Advance(Transition{
		OperationID: "legacy-v2-drain", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	newConfig, err := Render(template, newState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	return operationJournalV2{
		SchemaVersion: rollbackOperationJournalSchemaVersion,
		OperationID:   "legacy-v2-drain",
		Phase:         phase,
		Action:        "transfer",
		Host:          "versiond-2",
		MembershipID:  result.MembershipID,
		From:          HostActive,
		To:            HostDraining,
		Target:        HostOffline,
		Result:        "advanced",
		OldState:      &oldState,
		NewState:      newState,
		OldReceipts:   &receipts,
		NewReceipts:   receipts.Clone(),
		OldConfig:     oldConfig,
		NewConfigSHA:  hashBytes(newConfig),
		Reload:        true,
		CreatedAt:     time.Now().UTC(),
	}, newState, oldConfig, newConfig
}

func operationJournalV3FromCurrent(current operationJournal) operationJournalV3 {
	receipts := newReceiptIndex()
	if current.NewReceipts != nil {
		receipts = current.NewReceipts.Clone()
	}
	if current.Receipt != nil {
		_ = receipts.Record(current.OperationID, *current.Receipt)
	}
	return operationJournalV3{
		SchemaVersion:  fixedConfigOperationJournalSchemaVersion,
		OperationID:    current.OperationID,
		Phase:          current.Phase,
		Action:         current.Action,
		Host:           current.Host,
		MembershipID:   current.MembershipID,
		From:           current.From,
		To:             current.To,
		Target:         current.Target,
		Result:         current.Result,
		OldState:       current.OldState,
		NewState:       current.NewState,
		OldReceipts:    current.OldReceipts,
		NewReceipts:    receipts,
		OldConfig:      current.OldConfig,
		NewConfig:      current.NewConfig,
		NewConfigSHA:   current.NewConfigSHA,
		Audit:          current.Audit,
		RecoveryPolicy: current.RecoveryPolicy,
		Reload:         current.Reload,
		CreatedAt:      current.CreatedAt,
	}
}

func operationJournalV4FromCurrent(current operationJournal) operationJournalV4 {
	receipts := newReceiptIndex()
	if current.NewReceipts != nil {
		receipts = current.NewReceipts.Clone()
	}
	if current.Receipt != nil {
		_ = receipts.Record(current.OperationID, *current.Receipt)
	}
	return operationJournalV4{
		SchemaVersion:   receiptSnapshotJournalSchemaVersion,
		OperationID:     current.OperationID,
		Phase:           current.Phase,
		Action:          current.Action,
		Host:            current.Host,
		MembershipID:    current.MembershipID,
		From:            current.From,
		To:              current.To,
		Target:          current.Target,
		Result:          current.Result,
		OldState:        current.OldState,
		NewState:        current.NewState,
		OldReceipts:     current.OldReceipts,
		NewReceipts:     receipts,
		OldConfig:       current.OldConfig,
		NewConfig:       current.NewConfig,
		NewConfigSHA:    current.NewConfigSHA,
		RenderRevision:  current.RenderRevision,
		RenderSourceSHA: current.RenderSourceSHA,
		Audit:           current.Audit,
		RecoveryPolicy:  current.RecoveryPolicy,
		Reload:          current.Reload,
		CreatedAt:       current.CreatedAt,
	}
}

func stagePendingDrain(
	t *testing.T,
	paths Config,
	oldState State,
	phase string,
) (State, []byte, []byte) {
	t.Helper()
	oldConfig, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	newState := oldState.Clone()
	result, err := newState.Advance(Transition{
		OperationID: "interrupted", Host: "versiond-2",
		From: HostActive, To: HostDraining, Target: HostOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	newConfig, err := Render(template, newState, DefaultProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	receiptsData, err := os.ReadFile(paths.ReceiptsPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipts receiptIndex
	if err := json.Unmarshal(receiptsData, &receipts); err != nil {
		t.Fatal(err)
	}
	change := mutation{
		OperationID:  "interrupted",
		Action:       "transfer",
		Host:         "versiond-2",
		MembershipID: result.MembershipID,
		From:         HostActive,
		To:           HostDraining,
		Target:       HostOffline,
		Result:       "advanced",
	}
	journal := operationJournal{
		SchemaVersion:   operationJournalSchemaVersion,
		OperationID:     change.OperationID,
		Phase:           phase,
		Action:          change.Action,
		Host:            change.Host,
		MembershipID:    change.MembershipID,
		From:            change.From,
		To:              change.To,
		Target:          change.Target,
		Result:          change.Result,
		NewState:        newState,
		NewConfig:       newConfig,
		NewConfigSHA:    hashBytes(newConfig),
		RenderRevision:  1,
		RenderSourceSHA: renderSourceSHA(template, DefaultProxyPolicy()),
		Audit:           auditRecord(change, newState.Generation, hashBytes(newConfig)),
		RecoveryPolicy:  recoveryPolicyRollForward,
		Reload:          true,
		CreatedAt:       time.Now().UTC(),
	}
	if err := writeJSONAtomic(paths.JournalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}
	switch phase {
	case operationPhaseIntentCommitted:
	case operationPhaseDesiredPersisted:
		if err := writeJSONAtomic(paths.StatePath, newState, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(paths.ReceiptsPath, receipts, 0o600); err != nil {
			t.Fatal(err)
		}
	case operationPhaseApplied, operationPhaseAuditEnqueued:
		if err := writeJSONAtomic(paths.StatePath, newState, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(paths.ReceiptsPath, receipts, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(paths.OutputPath, newConfig, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(paths.AppliedPath, appliedState{
			SchemaVersion: appliedStateSchemaVersion,
			Generation:    newState.Generation,
			ConfigSHA:     hashBytes(newConfig),
			OperationID:   "interrupted",
			AppliedAt:     time.Now().UTC(),
		}, 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported staged phase %q", phase)
	}
	return newState, oldConfig, newConfig
}

func newTestController(t *testing.T) (*Controller, *fakeRunner, Config) {
	t.Helper()
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "nginx.conf.template")
	if err := os.WriteFile(templatePath, []byte(testTemplate), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{
		StatePath:       filepath.Join(dir, "state.json"),
		AppliedPath:     filepath.Join(dir, "applied.json"),
		ReceiptsPath:    filepath.Join(dir, "receipts.json"),
		AuditPath:       filepath.Join(dir, "audit.jsonl"),
		AuditOutboxPath: filepath.Join(dir, "audit-outbox.json"),
		LockPath:        filepath.Join(dir, "router.lock"),
		JournalPath:     filepath.Join(dir, "operation.json"),
		TemplatePath:    templatePath,
		OutputPath:      filepath.Join(dir, "default.conf"),
		NginxBinary:     "nginx",
	}
	runner := &fakeRunner{outputPath: config.OutputPath}
	return NewController(config, runner), runner, config
}

func newTestState(t *testing.T) State {
	t.Helper()
	state, err := NewState([]string{"versiond-1", "versiond-2"}, 8080, "versiond-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	state.LastOperation = fmt.Sprintf("bootstrap-%d", time.Now().UnixNano())
	return state
}

func staticState(state State) InitialStateFactory {
	return func() (State, error) {
		return state, nil
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file %s changed unexpectedly\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func countRunnerCalls(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}
