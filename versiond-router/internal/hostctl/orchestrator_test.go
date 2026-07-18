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
	mu                 sync.Mutex
	calls              []string
	running            bool
	runningProbeErrors int
	stopOnTerm         bool
	health             HealthSummary
	restartPolicyJSON  string
	stopTimeout        string
	activateErrors     int
}

func (r *fakeRemote) Run(_ context.Context, destination string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	command := destination + ": " + strings.Join(args, " ")
	r.calls = append(r.calls, command)
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "gonka-routerctl host activate") && r.activateErrors > 0:
		r.activateErrors--
		return "", errors.New("transient router activation failure")
	case strings.Contains(joined, "/healthz?summary=1"):
		data, _ := json.Marshal(r.health)
		return string(data), nil
	case strings.Contains(joined, "HostConfig.RestartPolicy"):
		if r.restartPolicyJSON != "" {
			return r.restartPolicyJSON + "\n", nil
		}
		return `{"Name":"unless-stopped","MaximumRetryCount":0}` + "\n", nil
	case strings.Contains(joined, "Config.StopTimeout"):
		if r.stopTimeout != "" {
			return r.stopTimeout + "\n", nil
		}
		return "1800\n", nil
	case strings.Contains(joined, ".State.Running"):
		if r.runningProbeErrors > 0 {
			r.runningProbeErrors--
			return "", errors.New("transient status failure")
		}
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
		"gonka-routerctl host drain",
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
			Accepting: true, Available: true, Reconciled: true,
			InflightKnown: true, Idle: true,
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

func TestReplaceRestoresPolicyFromEvacuationJournal(t *testing.T) {
	remote := &fakeRemote{
		health: HealthSummary{
			SchemaVersion: 1, State: "serving", Ready: true,
			Accepting: true, Available: true, Reconciled: true,
			InflightKnown: true, Idle: true,
		},
	}
	orchestrator := newTestOrchestrator(t, remote, "replace-policy")
	orchestrator.config.DockerRestartPolicy = ""
	orchestrator.config.EvacuationJournal = filepath.Join(t.TempDir(), "evacuation.json")
	evacuationScope := orchestrator.operationScope()
	evacuationScope.VersiondSSH = "retired-versiond-host"
	evacuation := Journal{
		SchemaVersion:         1,
		OperationID:           "evacuated-policy",
		Mode:                  "evacuate",
		Scope:                 evacuationScope,
		Phase:                 "complete",
		PreviousRestartPolicy: "always",
		UpdatedAt:             time.Now().UTC(),
	}
	data, err := json.Marshal(evacuation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orchestrator.config.EvacuationJournal, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Replace(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := remote.callLog(); !strings.Contains(calls, "docker update --restart=always") {
		t.Fatalf("replacement did not restore evacuation policy:\n%s", calls)
	}
	assertJournalRestartPolicy(t, orchestrator.config.JournalPath, "always")
}

func TestReplaceRejectsPolicyFromAnotherLogicalHost(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "replace-wrong-host")
	orchestrator.config.DockerRestartPolicy = ""
	orchestrator.config.EvacuationJournal = filepath.Join(t.TempDir(), "evacuation.json")
	scope := orchestrator.operationScope()
	scope.VersiondService = "another-versiond"
	evacuation := Journal{
		SchemaVersion:         1,
		OperationID:           "another-host",
		Mode:                  "evacuate",
		Scope:                 scope,
		Phase:                 "complete",
		PreviousRestartPolicy: "always",
		UpdatedAt:             time.Now().UTC(),
	}
	data, err := json.Marshal(evacuation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orchestrator.config.EvacuationJournal, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Replace(context.Background()); err == nil {
		t.Fatal("replacement accepted restart policy from another logical host")
	}
	if calls := remote.callLog(); strings.Contains(calls, "host join") {
		t.Fatalf("replacement mutated router before journal validation:\n%s", calls)
	}
}

func TestDockerRestartPolicyPreservesRetryCount(t *testing.T) {
	remote := &fakeRemote{
		restartPolicyJSON: `{"Name":"on-failure","MaximumRetryCount":7}`,
	}
	orchestrator := newTestOrchestrator(t, remote, "restart-retries")
	policy, err := orchestrator.dockerRestartPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy != "on-failure:7" {
		t.Fatalf("restart policy = %q, want on-failure:7", policy)
	}
}

func TestReplaceRequiresRestartPolicySource(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "replace-no-policy")
	orchestrator.config.DockerRestartPolicy = ""
	if err := orchestrator.Replace(context.Background()); err == nil {
		t.Fatal("replacement without a restart-policy source succeeded")
	}
	if calls := remote.callLog(); strings.Contains(calls, "host join") {
		t.Fatalf("replacement changed router state before policy validation:\n%s", calls)
	}
}

func TestCancelEvacuationRestoresPolicyBeforeReactivating(t *testing.T) {
	remote := &fakeRemote{running: true}
	orchestrator := newTestOrchestrator(t, remote, "cancel")
	journal := Journal{
		SchemaVersion:         1,
		OperationID:           "cancel",
		Mode:                  "evacuate",
		Scope:                 orchestrator.operationScope(),
		Phase:                 "restart_disabled",
		PreviousRestartPolicy: "always",
		UpdatedAt:             time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.CancelEvacuation(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCallOrder(
		t,
		remote.callLog(),
		"docker update --restart=always",
		"gonka-routerctl host activate",
	)
	assertJournalPhase(t, orchestrator.config.JournalPath, "canceled")
}

func TestCancelEvacuationResumesItsDurableCompensation(t *testing.T) {
	remote := &fakeRemote{running: true, activateErrors: 1}
	orchestrator := newTestOrchestrator(t, remote, "cancel-resume")
	journal := Journal{
		SchemaVersion:         1,
		OperationID:           "cancel-resume",
		Mode:                  "evacuate",
		Scope:                 orchestrator.operationScope(),
		Phase:                 "restart_disabled",
		PreviousRestartPolicy: "always",
		UpdatedAt:             time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.CancelEvacuation(context.Background()); err == nil {
		t.Fatal("cancellation succeeded despite router activation failure")
	}
	assertJournalCancellationPhase(t, orchestrator.config.JournalPath, "restart_restored")
	if err := orchestrator.Evacuate(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "cancellation is in progress") {
		t.Fatalf("evacuation resumed through cancellation: %v", err)
	}
	if calls := remote.callLog(); strings.Contains(calls, "docker kill --signal TERM") {
		t.Fatalf("evacuation signaled versiond during cancellation:\n%s", calls)
	}

	if err := orchestrator.CancelEvacuation(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "canceled")
}

func TestEvacuationReassertsDisabledRestartPolicyBeforeSignal(t *testing.T) {
	remote := &fakeRemote{
		running:    true,
		stopOnTerm: true,
		health: HealthSummary{
			SchemaVersion: 1, InflightKnown: true, Idle: true,
		},
	}
	orchestrator := newTestOrchestrator(t, remote, "restart-reassert")
	journal := Journal{
		SchemaVersion:         1,
		OperationID:           "restart-reassert",
		Mode:                  "evacuate",
		Scope:                 orchestrator.operationScope(),
		Phase:                 "restart_disabled",
		PreviousRestartPolicy: "unless-stopped",
		UpdatedAt:             time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCallOrder(
		t,
		remote.callLog(),
		"docker update --restart=no",
		"docker kill --signal TERM",
	)
}

func TestReplacementActivationRequiresFullConvergence(t *testing.T) {
	health := HealthSummary{
		SchemaVersion: 1,
		State:         "serving",
		Ready:         true,
		Accepting:     true,
		Available:     true,
		Progressing:   true,
	}
	if health.readyForActivation() {
		t.Fatal("progressing replacement was ready for router activation")
	}
	health.Progressing = false
	health.Reconciled = true
	if !health.readyForActivation() {
		t.Fatal("fully reconciled replacement was not ready for router activation")
	}
}

func TestProbeFailureBeforeSignalIntentCanBeCanceled(t *testing.T) {
	remote := &fakeRemote{
		running:            true,
		runningProbeErrors: 1,
		health: HealthSummary{
			SchemaVersion: 1, InflightKnown: true, Idle: true,
		},
	}
	orchestrator := newTestOrchestrator(t, remote, "cancel-probe-failure")
	if err := orchestrator.Evacuate(context.Background()); err == nil {
		t.Fatal("evacuation succeeded despite the injected process probe failure")
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "restart_disabled")
	if calls := remote.callLog(); strings.Contains(calls, "docker kill --signal TERM") {
		t.Fatalf("evacuation sent SIGTERM after a failed process probe:\n%s", calls)
	}

	if err := orchestrator.CancelEvacuation(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "canceled")
}

func TestCancelEvacuationRejectsRequestedSignal(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "cancel-late")
	journal := Journal{
		SchemaVersion: 1,
		OperationID:   "cancel-late",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "term_requested",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.CancelEvacuation(context.Background()); err == nil {
		t.Fatal("cancellation after SIGTERM succeeded")
	}
	if calls := remote.callLog(); calls != "" {
		t.Fatalf("late cancellation issued remote commands:\n%s", calls)
	}
}

func TestWaitForStoppedRetriesTransientProbeFailure(t *testing.T) {
	remote := &fakeRemote{runningProbeErrors: 1}
	orchestrator := newTestOrchestrator(t, remote, "probe-retry")
	stopped, err := orchestrator.waitForStopped(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("stopped host was not detected after a transient probe failure")
	}
}

type blockingRemote struct{}

func (blockingRemote) Run(ctx context.Context, _ string, _ ...string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestRemoteCommandTimeout(t *testing.T) {
	orchestrator := newTestOrchestrator(t, blockingRemote{}, "command-timeout")
	orchestrator.config.CommandTimeout = 5 * time.Millisecond
	err := orchestrator.routerTransition(context.Background(), "drain")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("remote command error = %v, want deadline exceeded", err)
	}
}

func TestEvacuateRejectsShortRuntimeStopContractBeforeRouterMutation(t *testing.T) {
	remote := &fakeRemote{stopTimeout: "10"}
	orchestrator := newTestOrchestrator(t, remote, "short-stop")
	orchestrator.config.KillGrace = 30 * time.Minute
	if err := orchestrator.Evacuate(context.Background()); err == nil {
		t.Fatal("evacuation accepted a ten-second Docker stop timeout")
	}
	if calls := remote.callLog(); strings.Contains(calls, "gonka-routerctl host drain") {
		t.Fatalf("router changed before runtime preflight completed:\n%s", calls)
	}
}

func TestParseSystemdTimeSpan(t *testing.T) {
	got, err := parseSystemdTimeSpan("25min 30s")
	if err != nil {
		t.Fatal(err)
	}
	if want := 25*time.Minute + 30*time.Second; got != want {
		t.Fatalf("duration = %s, want %s", got, want)
	}
	got, err = parseSystemdTimeSpan("infinity")
	if err != nil || got < 100*365*24*time.Hour {
		t.Fatalf("infinity = %s, %v", got, err)
	}
	got, err = parseSystemdTimeSpan("1y 2months 3days")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(365.25*float64(24*time.Hour)) +
		2*time.Duration(30.44*float64(24*time.Hour)) +
		3*24*time.Hour
	if got != want {
		t.Fatalf("extended duration = %s, want %s", got, want)
	}
}

func TestSystemdRuntimeValidatesContractAndUsesManagedStop(t *testing.T) {
	var calls []string
	runtime := newServiceRuntime(
		RuntimeSystemd,
		"versiond.service",
		func(_ context.Context, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			if len(args) >= 2 && args[0] == "systemctl" && args[1] == "show" {
				return "TimeoutStopUSec=30min\nKillMode=control-group\nSendSIGKILL=yes\n", nil
			}
			return "", nil
		},
	)
	if err := runtime.ValidateStopContract(context.Background(), 25*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Signal(context.Background(), "TERM"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "systemctl stop --no-block versiond.service") {
		t.Fatalf("systemd runtime did not use a managed stop job:\n%s", joined)
	}
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

func assertJournalRestartPolicy(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.PreviousRestartPolicy != want {
		t.Fatalf(
			"journal restart policy = %q, want %q",
			journal.PreviousRestartPolicy,
			want,
		)
	}
}

func assertJournalCancellationPhase(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.CancellationPhase != want {
		t.Fatalf(
			"journal cancellation phase = %q, want %q",
			journal.CancellationPhase,
			want,
		)
	}
}
