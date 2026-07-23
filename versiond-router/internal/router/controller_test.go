package router

import (
	"bytes"
	"context"
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
	mu             sync.Mutex
	calls          []string
	failReloadOnce bool
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
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

func TestControllerReloadFailureRollsBackConfigAndState(t *testing.T) {
	controller, runner, paths := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(state)); err != nil {
		t.Fatal(err)
	}
	oldConfig, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	oldState, err := os.ReadFile(paths.StatePath)
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
	assertFileEquals(t, paths.OutputPath, oldConfig)
	assertFileEquals(t, paths.StatePath, oldState)
	if _, err := os.Stat(paths.JournalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back journal should be removed, stat error = %v", err)
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

func TestControllerRemoveReloadFailureRestoresOfflineHost(t *testing.T) {
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
	oldConfig, err := os.ReadFile(paths.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	oldState, err := os.ReadFile(paths.StatePath)
	if err != nil {
		t.Fatal(err)
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
	assertFileEquals(t, paths.OutputPath, oldConfig)
	assertFileEquals(t, paths.StatePath, oldState)
}

func TestControllerCompletedOperationDoesNotRunAgain(t *testing.T) {
	controller, runner, _ := newTestController(t)
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

func TestControllerStatusReportsPendingReloadWithoutSideEffects(t *testing.T) {
	controller, runner, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(oldState)); err != nil {
		t.Fatal(err)
	}
	newState, _, newConfig := stagePendingDrain(t, paths, oldState, "reloaded")
	runner.mu.Lock()
	callsBefore := len(runner.calls)
	runner.mu.Unlock()

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != oldState.Generation {
		t.Fatalf("committed generation = %d, want %d", status.Generation, oldState.Generation)
	}
	if status.PendingOperation == nil || status.PendingOperation.Phase != "reloaded" {
		t.Fatalf("pending operation = %#v, want reloaded phase", status.PendingOperation)
	}
	if status.PendingOperation.MembershipID == "" ||
		status.PendingOperation.From != HostActive ||
		status.PendingOperation.To != HostDraining ||
		status.PendingOperation.Target != HostOffline {
		t.Fatalf("pending FSM edge = %#v", status.PendingOperation)
	}
	assertFileEquals(t, paths.OutputPath, newConfig)
	runner.mu.Lock()
	callsAfter := len(runner.calls)
	runner.mu.Unlock()
	if callsAfter != callsBefore {
		t.Fatalf("status executed %d command(s)", callsAfter-callsBefore)
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

func TestControllerRecoveryRollsBackBeforeReload(t *testing.T) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(oldState)); err != nil {
		t.Fatal(err)
	}
	_, oldConfig, _ := stagePendingDrain(t, paths, oldState, "config_published")

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != oldState.Generation {
		t.Fatalf("recovered generation = %d, want %d", recovered.Generation, oldState.Generation)
	}
	assertFileEquals(t, paths.OutputPath, oldConfig)
}

func TestControllerRecoveryRejectsChangedReloadedConfig(t *testing.T) {
	controller, _, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), staticState(oldState)); err != nil {
		t.Fatal(err)
	}
	stagePendingDrain(t, paths, oldState, "reloaded")
	if err := writeFileAtomic(paths.OutputPath, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Recover(context.Background()); err == nil {
		t.Fatal("expected recovery to reject a changed published config")
	}
	if _, err := os.Stat(paths.JournalPath); err != nil {
		t.Fatalf("failed recovery removed its journal: %v", err)
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
	if err := writeFileAtomic(paths.OutputPath, newConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	journal := operationJournal{
		SchemaVersion: SchemaVersion,
		OperationID:   "interrupted",
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
		OldConfig:     oldConfig,
		NewConfigSHA:  hashBytes(newConfig),
		Reload:        true,
		CreatedAt:     time.Now().UTC(),
	}
	if err := writeJSONAtomic(paths.JournalPath, journal, 0o600); err != nil {
		t.Fatal(err)
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
		StatePath:    filepath.Join(dir, "state.json"),
		AuditPath:    filepath.Join(dir, "audit.jsonl"),
		LockPath:     filepath.Join(dir, "router.lock"),
		JournalPath:  filepath.Join(dir, "operation.json"),
		TemplatePath: templatePath,
		OutputPath:   filepath.Join(dir, "default.conf"),
		NginxBinary:  "nginx",
	}
	runner := &fakeRunner{}
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
