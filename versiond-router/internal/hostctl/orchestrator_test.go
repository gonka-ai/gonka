package hostctl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeRemote struct {
	mu         sync.Mutex
	calls      []string
	running    bool
	stopOnTerm bool
	health     HealthSummary
}

func (r *fakeRemote) Run(_ context.Context, destination string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	command := destination + ": " + strings.Join(args, " ")
	r.calls = append(r.calls, command)
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "/healthz?summary=1"):
		data, _ := json.Marshal(r.health)
		return string(data), nil
	case strings.Contains(joined, "HostConfig.RestartPolicy.Name"):
		return "unless-stopped\n", nil
	case strings.Contains(joined, ".State.Running"):
		if r.running {
			return "true\n", nil
		}
		return "false\n", nil
	case strings.Contains(joined, "docker kill --signal TERM"):
		if r.stopOnTerm {
			r.running = false
		}
	case strings.Contains(joined, "docker kill --signal KILL"):
		r.running = false
	case strings.Contains(joined, "docker start"):
		r.running = true
	}
	return "{}", nil
}

func TestEvacuateOrdersRouterDrainBeforeVersiondStop(t *testing.T) {
	remote := &fakeRemote{
		running:    true,
		stopOnTerm: true,
		health: HealthSummary{
			SchemaVersion: 1, State: "serving", Ready: true,
			Accepting: true, InflightKnown: true, Idle: true,
		},
	}
	orchestrator := newTestOrchestrator(t, remote, "evacuate-order")
	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(t, calls,
		"gonka-routerctl host drain",
		"/healthz?summary=1",
		"docker update --restart=no",
		"docker kill --signal TERM",
		"gonka-routerctl host offline",
	)
	if strings.Contains(calls, "docker kill --signal KILL") {
		t.Fatalf("graceful evacuation unexpectedly used SIGKILL:\n%s", calls)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "complete")
}

func TestEvacuateUsesSIGKILLOnlyAfterGrace(t *testing.T) {
	remote := &fakeRemote{
		running: true,
		health: HealthSummary{
			SchemaVersion: 1, State: "serving", Ready: true,
			Accepting: true, InflightKnown: true, Idle: true,
		},
	}
	orchestrator := newTestOrchestrator(t, remote, "evacuate-force")
	orchestrator.config.KillGrace = 5 * time.Millisecond
	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := remote.callLog()
	assertCallOrder(t, calls, "docker kill --signal TERM", "docker kill --signal KILL")
}

func TestReplaceKeepsJoiningHostDownUntilReady(t *testing.T) {
	remote := &fakeRemote{
		health: HealthSummary{
			SchemaVersion: 1, State: "serving", Ready: true,
			Accepting: true, InflightKnown: true, Idle: true,
		},
	}
	orchestrator := newTestOrchestrator(t, remote, "replace-order")
	orchestrator.config.UpstreamAddress = "replacement-2"
	if err := orchestrator.Replace(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := remote.callLog()
	assertCallOrder(t, calls,
		"gonka-routerctl host join --operation-id replace-order --address replacement-2 versiond-2",
		"docker update --restart=unless-stopped",
		"docker start",
		"/healthz?summary=1",
		"gonka-routerctl host activate",
	)
}

func TestEvacuateResumesFromCheckpoint(t *testing.T) {
	remote := &fakeRemote{
		health: HealthSummary{SchemaVersion: 1, InflightKnown: true, Idle: true},
	}
	orchestrator := newTestOrchestrator(t, remote, "resume")
	journal := Journal{
		SchemaVersion: 1,
		OperationID:   "resume",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "host_stopped",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := remote.callLog()
	if strings.Contains(calls, "host drain") || strings.Contains(calls, "docker kill") {
		t.Fatalf("resumed operation repeated completed phases:\n%s", calls)
	}
	if !strings.Contains(calls, "host offline") {
		t.Fatalf("resumed operation did not finish router offline phase:\n%s", calls)
	}
}

func TestResumeRejectsChangedOperationScope(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "scope")
	journal := Journal{
		SchemaVersion: 1,
		OperationID:   "scope",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "router_draining",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}
	orchestrator.config.VersiondService = "another-versiond"
	if err := orchestrator.Evacuate(context.Background()); err == nil {
		t.Fatal("resume with a changed operation scope was accepted")
	}
}

func TestOperationLockHonorsContext(t *testing.T) {
	orchestrator := newTestOrchestrator(t, &fakeRemote{}, "locked")
	lock, err := os.OpenFile(
		orchestrator.config.JournalPath+".lock",
		os.O_CREATE|os.O_RDWR,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = orchestrator.withOperationLock(ctx, func() error {
		t.Fatal("operation ran while its lock was held")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock error = %v, want context deadline", err)
	}
}

func newTestOrchestrator(t *testing.T, remote Remote, operationID string) *Orchestrator {
	t.Helper()
	orchestrator, err := New(Config{
		RouterSSH:           "router-host",
		RouterRuntime:       RuntimeDocker,
		RouterService:       "versiond-router",
		Upstream:            "versiond-2",
		VersiondSSH:         "versiond-host",
		VersiondRuntime:     RuntimeDocker,
		VersiondService:     "versiond-2",
		OperationID:         operationID,
		JournalPath:         filepath.Join(t.TempDir(), operationID+".json"),
		DrainTimeout:        50 * time.Millisecond,
		PollInterval:        time.Millisecond,
		KillGrace:           50 * time.Millisecond,
		DockerRestartPolicy: "unless-stopped",
	}, remote)
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func (r *fakeRemote) callLog() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, "\n")
}

func assertCallOrder(t *testing.T, calls string, fragments ...string) {
	t.Helper()
	position := -1
	for _, fragment := range fragments {
		next := strings.Index(calls[position+1:], fragment)
		if next < 0 {
			t.Fatalf("call %q missing or out of order:\n%s", fragment, calls)
		}
		position += next + 1
	}
}

func assertJournalPhase(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Phase != want {
		t.Fatalf("journal phase = %q, want %q", journal.Phase, want)
	}
}
