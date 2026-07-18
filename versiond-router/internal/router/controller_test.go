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
	if _, err := controller.Bootstrap(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	updated, err := controller.Transition(context.Background(), Transition{
		Action: ActionDrain, Host: "versiond-2", OperationID: "drain-2",
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
	if !bytes.Contains(config, []byte("server versiond-2:8080 resolve down;")) {
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
	if _, err := controller.Bootstrap(context.Background(), state); err != nil {
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
		Action: ActionDrain, Host: "versiond-2", OperationID: "failed-drain",
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

func TestControllerStatusReportsPendingReloadWithoutSideEffects(t *testing.T) {
	controller, runner, paths := newTestController(t)
	oldState := newTestState(t)
	if _, err := controller.Bootstrap(context.Background(), oldState); err != nil {
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
	if _, err := controller.Bootstrap(context.Background(), oldState); err != nil {
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
	if _, err := controller.Bootstrap(context.Background(), oldState); err != nil {
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
	if _, _, _, err := newState.Apply(Transition{
		Action: ActionDrain, Host: "versiond-2", OperationID: "interrupted",
	}); err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(paths.TemplatePath)
	if err != nil {
		t.Fatal(err)
	}
	newConfig, err := Render(template, newState)
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
		Action:        string(ActionDrain),
		Host:          "versiond-2",
		From:          HostActive,
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
