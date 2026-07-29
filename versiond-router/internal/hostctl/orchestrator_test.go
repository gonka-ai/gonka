package hostctl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"versiond-router/internal/router"
)

type fakeRemote struct {
	mu                 sync.Mutex
	calls              []string
	running            bool
	runningProbeErrors int
	stopOnTerm         bool
	ready              bool
	readinessErrors    int
	restartPolicyJSON  string
	stopTimeout        string
	activateErrors     int
	routerHostState    router.HostState
	routerTransfer     *router.Transfer
	routerHostMissing  bool
	routerOperation    *router.OperationLookup
	runtimeAbsent      bool
	removeOnRestartPin bool
	removeOnStopCheck  bool
}

func (r *fakeRemote) Run(_ context.Context, destination string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	command := destination + ": " + strings.Join(args, " ")
	r.calls = append(r.calls, command)
	joined := strings.Join(args, " ")
	switch {
	case (strings.Contains(joined, "gonka-routerctl host cancel") ||
		strings.Contains(joined, "--to active")) && r.activateErrors > 0:
		r.activateErrors--
		return "", errors.New("transient router activation failure")
	case strings.Contains(joined, "gonka-routerctl operation status"):
		lookup := router.OperationLookup{
			OperationID: argumentValue(args, "--operation-id"),
		}
		if r.routerOperation != nil {
			lookup = *r.routerOperation
		}
		data, err := json.Marshal(lookup)
		return string(data), err
	case strings.Contains(joined, "gonka-routerctl"):
		if strings.Contains(joined, "--to removed") {
			return fakeRouterState(false), nil
		}
		if strings.Contains(joined, "gonka-routerctl status") &&
			r.routerHostMissing {
			return fakeRouterState(false), nil
		}
		if strings.Contains(joined, "gonka-routerctl status") &&
			r.routerHostState != "" {
			return fakeRouterStateWithTransfer(
				true,
				r.routerHostState,
				r.routerTransfer,
			), nil
		}
		return fakeRouterState(true), nil
	case strings.Contains(joined, "127.0.0.1:8081/ready"):
		if r.readinessErrors > 0 {
			r.readinessErrors--
			return "", errors.New("versiond is not ready")
		}
		if !r.ready {
			return "", errors.New("versiond is not ready")
		}
		return "", nil
	case strings.Contains(joined, "HostConfig.RestartPolicy"):
		if r.restartPolicyJSON != "" {
			return r.restartPolicyJSON + "\n", nil
		}
		return `{"Name":"unless-stopped","MaximumRetryCount":0}` + "\n", nil
	case strings.Contains(joined, "Config.StopTimeout"):
		if r.removeOnStopCheck {
			r.runtimeAbsent = true
			r.running = false
			return "", errors.New(
				"docker: Error response from daemon: No such container: versiond-2",
			)
		}
		if r.stopTimeout != "" {
			return r.stopTimeout + "\n", nil
		}
		return "1800\n", nil
	case strings.Contains(joined, ".State.Running"):
		if r.runningProbeErrors > 0 {
			r.runningProbeErrors--
			return "", errors.New("transient status failure")
		}
		if r.runtimeAbsent {
			return "", errors.New(
				"docker: Error response from daemon: No such container: versiond-2",
			)
		}
		if r.running {
			return "true\n", nil
		}
		return "false\n", nil
	case strings.Contains(joined, "docker update --restart=no") &&
		r.removeOnRestartPin:
		r.runtimeAbsent = true
		r.running = false
		return "", errors.New(
			"docker: Error response from daemon: No such container: versiond-2",
		)
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

func argumentValue(args []string, name string) string {
	for i := range args {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func fakeRouterState(includeTarget bool) string {
	return fakeRouterStateWithTarget(includeTarget, router.HostActive)
}

func fakeRouterStateWithTarget(includeTarget bool, targetState router.HostState) string {
	return fakeRouterStateWithTransfer(includeTarget, targetState, nil)
}

func fakeRouterStateWithTransfer(
	includeTarget bool,
	targetState router.HostState,
	transfer *router.Transfer,
) string {
	hosts := []router.Host{{
		MembershipID: "membership-versiond-1",
		Name:         "versiond-1",
		Address:      "versiond-1",
		State:        router.HostActive,
	}}
	if includeTarget {
		hosts = append(hosts, router.Host{
			MembershipID: "membership-versiond-2",
			Name:         "versiond-2",
			Address:      "versiond-2",
			State:        targetState,
		})
	}
	state := router.State{
		SchemaVersion:  router.SchemaVersion,
		Generation:     1,
		Port:           8080,
		LegacyHost:     "versiond-1",
		NonHAVersions:  []string{},
		Hosts:          hosts,
		ActiveTransfer: transfer,
		UpdatedAt:      time.Now().UTC(),
	}
	data, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestEvacuateOrdersRouterDrainBeforeVersiondStop(t *testing.T) {
	remote := &fakeRemote{
		running:    true,
		stopOnTerm: true,
	}
	orchestrator := newTestOrchestrator(t, remote, "evacuate-order")
	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(t, calls,
		"--from active --to draining --target offline",
		"--from draining --to stopping --target offline",
		"docker update --restart=no",
		"docker kill --signal TERM",
		"--from stopping --to offline --target offline",
	)
	if strings.Contains(calls, "docker kill --signal KILL") {
		t.Fatalf("graceful evacuation unexpectedly used SIGKILL:\n%s", calls)
	}
	if strings.Contains(calls, "/healthz") {
		t.Fatalf("evacuation used health as a control-plane API:\n%s", calls)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "complete")
}

func TestStopWorkflowRejectsInitiallyAbsentRuntime(t *testing.T) {
	tests := []struct {
		mode string
		run  func(*Orchestrator, context.Context) error
	}{
		{
			mode: "evacuate",
			run: func(o *Orchestrator, ctx context.Context) error {
				return o.Evacuate(ctx)
			},
		},
		{
			mode: "decommission",
			run: func(o *Orchestrator, ctx context.Context) error {
				return o.Decommission(ctx)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			remote := &fakeRemote{runtimeAbsent: true}
			orchestrator := newTestOrchestrator(
				t,
				remote,
				tt.mode+"-absent",
			)

			err := tt.run(orchestrator, context.Background())
			if err == nil ||
				!strings.Contains(err.Error(), "--allow-absent-runtime") {
				t.Fatalf(
					"%s error = %v, want explicit absent-runtime rejection",
					tt.mode,
					err,
				)
			}
			calls := remote.callLog()
			if strings.Contains(calls, "--from active") ||
				strings.Contains(calls, "docker update") ||
				strings.Contains(calls, "docker kill") {
				t.Fatalf(
					"initially absent runtime caused a side effect:\n%s",
					calls,
				)
			}
			assertJournalPhase(
				t,
				orchestrator.config.JournalPath,
				phaseStarted,
			)
		})
	}
}

func TestEvacuateAllowsExplicitlyAbsentRuntimeRecovery(t *testing.T) {
	remote := &fakeRemote{runtimeAbsent: true}
	orchestrator := newTestOrchestrator(t, remote, "evacuate-absent")

	if err := orchestrator.Evacuate(context.Background()); err == nil {
		t.Fatal("initially absent runtime was accepted without an override")
	}
	orchestrator.config.AllowAbsentRuntime = true

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"--from active --to draining --target offline",
		"--from draining --to stopping --target offline",
		"--from stopping --to offline --target offline",
	)
	for _, unexpected := range []string{
		"Config.StopTimeout",
		"HostConfig.RestartPolicy",
		"docker update",
		"docker kill",
	} {
		if strings.Contains(calls, unexpected) {
			t.Fatalf(
				"absent runtime used %q instead of converging router state:\n%s",
				unexpected,
				calls,
			)
		}
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseComplete)
}

func TestEvacuateResumesAfterRuntimeRemovalBeforeSignalIntent(t *testing.T) {
	remote := &fakeRemote{runtimeAbsent: true}
	orchestrator := newTestOrchestrator(t, remote, "resume-absent-runtime")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
		OperationID:           orchestrator.config.OperationID,
		Mode:                  "evacuate",
		Scope:                 orchestrator.operationScope(),
		Phase:                 phaseRestartDisabled,
		PreviousRestartPolicy: "unless-stopped",
		UpdatedAt:             time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"--from draining --to stopping --target offline",
		"--from stopping --to offline --target offline",
	)
	if strings.Contains(calls, "Config.StopTimeout") ||
		strings.Contains(calls, "docker update") ||
		strings.Contains(calls, "docker kill") {
		t.Fatalf("resumed evacuation acted on an absent runtime:\n%s", calls)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseComplete)
}

func TestEvacuateConvergesWhenContainerDisappearsDuringRestartPin(t *testing.T) {
	remote := &fakeRemote{
		running:            true,
		removeOnRestartPin: true,
	}
	orchestrator := newTestOrchestrator(t, remote, "evacuate-restart-pin-race")

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"docker update --restart=no",
		"--from draining --to stopping --target offline",
		"--from stopping --to offline --target offline",
	)
	if strings.Contains(calls, "docker kill") {
		t.Fatalf("evacuation signaled a removed container:\n%s", calls)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseComplete)
}

func TestEvacuateConvergesWhenContainerDisappearsDuringStopCheck(t *testing.T) {
	remote := &fakeRemote{
		running:           true,
		removeOnStopCheck: true,
	}
	orchestrator := newTestOrchestrator(t, remote, "evacuate-stop-check-race")

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"Config.StopTimeout",
		"--from active --to draining --target offline",
		"--from stopping --to offline --target offline",
	)
	if strings.Contains(calls, "docker update") ||
		strings.Contains(calls, "docker kill") {
		t.Fatalf("evacuation acted on a container removed during preflight:\n%s", calls)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseComplete)
}

func TestDecommissionRemovesHostAfterGracefulStop(t *testing.T) {
	remote := &fakeRemote{
		running:    true,
		stopOnTerm: true,
	}
	orchestrator := newTestOrchestrator(t, remote, "decommission-order")
	if err := orchestrator.Decommission(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertCallOrder(
		t,
		remote.callLog(),
		"--from active --to draining --target removed",
		"--from draining --to stopping --target removed",
		"docker kill --signal TERM",
		"--from stopping --to offline --target removed",
		"--from offline --to removed --target removed",
	)
	assertJournal(t, orchestrator.config.JournalPath, "decommission", "complete")
}

func TestDecommissionResumesRemovalAfterOfflineCheckpoint(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "decommission-resume")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   "decommission-resume",
		Mode:          "decommission",
		Scope:         orchestrator.operationScope(),
		Phase:         "router_offline",
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Decommission(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := remote.callLog()
	if strings.Contains(calls, "--to draining") || strings.Contains(calls, "docker kill") {
		t.Fatalf("resumed decommission repeated stop phases:\n%s", calls)
	}
	if !strings.Contains(calls, "--from offline --to removed") {
		t.Fatalf("resumed decommission did not remove the host:\n%s", calls)
	}
}

func TestDecommissionAdoptsAlreadyEvacuatedHost(t *testing.T) {
	remote := &fakeRemote{routerHostState: router.HostOffline}
	orchestrator := newTestOrchestrator(t, remote, "decommission-offline")

	if err := orchestrator.Decommission(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"gonka-routerctl status",
		"docker update --restart=no",
		"--from offline --to removed --target removed",
	)
	if strings.Contains(calls, "--to draining") ||
		strings.Contains(calls, "docker kill") {
		t.Fatalf("offline decommission repeated evacuation:\n%s", calls)
	}
	assertJournal(t, orchestrator.config.JournalPath, "decommission", phaseComplete)
}

func TestDecommissionRemovesOfflineHostAfterContainerRemoval(t *testing.T) {
	remote := &fakeRemote{
		routerHostState: router.HostOffline,
		runtimeAbsent:   true,
	}
	orchestrator := newTestOrchestrator(
		t,
		remote,
		"decommission-offline-absent",
	)

	if err := orchestrator.Decommission(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"gonka-routerctl status",
		"docker inspect --format {{.State.Running}}",
		"--from offline --to removed --target removed",
	)
	if strings.Contains(calls, "docker update --restart=no") {
		t.Fatalf("absent container received a restart-policy update:\n%s", calls)
	}
	assertJournal(t, orchestrator.config.JournalPath, "decommission", phaseComplete)
}

func TestDecommissionRecoversCompletionWhenLocalJournalWasLost(t *testing.T) {
	operationID := "decommission-with-lost-journal"
	remote := &fakeRemote{
		routerHostMissing: true,
		routerOperation: &router.OperationLookup{
			OperationID: operationID,
			Completed:   true,
			Completion: &router.OperationCompletion{
				OperationID:  operationID,
				Action:       "transfer",
				Host:         "versiond-2",
				MembershipID: "membership-versiond-2",
				Target:       router.HostRemoved,
				Result:       "completed",
				CompletedAt:  time.Now().UTC(),
			},
		},
	}
	orchestrator := newTestOrchestrator(t, remote, operationID)

	if err := orchestrator.Decommission(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertJournal(t, orchestrator.config.JournalPath, "decommission", phaseComplete)
	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"gonka-routerctl status",
		"gonka-routerctl operation status",
	)
	if strings.Contains(calls, "docker inspect") ||
		strings.Contains(calls, "docker update") ||
		strings.Contains(calls, "docker kill") {
		t.Fatalf("receipt recovery touched the removed runtime:\n%s", calls)
	}
}

func TestDecommissionRejectsForeignOfflineTransferBeforeRuntimeMutation(t *testing.T) {
	remote := &fakeRemote{
		routerHostState: router.HostOffline,
		routerTransfer: &router.Transfer{
			ID:           "previous-decommission",
			MembershipID: "membership-versiond-2",
			Host:         "versiond-2",
			From:         router.HostActive,
			To:           router.HostRemoved,
			StartedAt:    time.Now().UTC(),
		},
	}
	orchestrator := newTestOrchestrator(
		t,
		remote,
		"new-decommission",
	)

	err := orchestrator.Decommission(context.Background())
	if !errors.Is(err, router.ErrHostOperation) {
		t.Fatalf("decommission error = %v, want ErrHostOperation", err)
	}
	calls := remote.callLog()
	if strings.Contains(calls, ".State.Running") ||
		strings.Contains(calls, "docker update") ||
		strings.Contains(calls, "--to removed") {
		t.Fatalf("foreign transfer caused a side effect:\n%s", calls)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseStarted)
}

func TestEvacuateAdoptsAlreadyOfflineHost(t *testing.T) {
	remote := &fakeRemote{routerHostState: router.HostOffline}
	orchestrator := newTestOrchestrator(t, remote, "evacuate-offline")

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := remote.callLog()
	assertCallOrder(
		t,
		calls,
		"gonka-routerctl status",
		"docker update --restart=no",
	)
	if strings.Contains(calls, "--to draining") ||
		strings.Contains(calls, "docker kill") {
		t.Fatalf("offline evacuation repeated process shutdown:\n%s", calls)
	}
	assertJournal(t, orchestrator.config.JournalPath, "evacuate", phaseComplete)
}

func TestDecommissionRejectsRunningOfflineHost(t *testing.T) {
	remote := &fakeRemote{
		running:         true,
		routerHostState: router.HostOffline,
	}
	orchestrator := newTestOrchestrator(
		t,
		remote,
		"decommission-offline-running",
	)

	err := orchestrator.Decommission(context.Background())
	if err == nil || !strings.Contains(err.Error(), "offline versiond host is still running") {
		t.Fatalf("decommission error = %v, want running-host rejection", err)
	}
	calls := remote.callLog()
	if strings.Contains(calls, "--to removed") {
		t.Fatalf("running offline host was removed:\n%s", calls)
	}
	if strings.Contains(calls, "docker update --restart=no") {
		t.Fatalf("running offline host restart policy was changed:\n%s", calls)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseStarted)
}

func TestCancelDecommissionUsesTheCompensationFSM(t *testing.T) {
	remote := &fakeRemote{running: true}
	orchestrator := newTestOrchestrator(t, remote, "decommission-cancel")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
		OperationID:           "decommission-cancel",
		Mode:                  "decommission",
		Scope:                 orchestrator.operationScope(),
		Phase:                 "restart_disabled",
		PreviousRestartPolicy: "always",
		UpdatedAt:             time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCallOrder(
		t,
		remote.callLog(),
		"docker update --restart=always",
		"gonka-routerctl host cancel",
	)
	assertJournal(t, orchestrator.config.JournalPath, "decommission", "canceled")
}

func TestAddKeepsNewHostDownUntilReady(t *testing.T) {
	remote := &fakeRemote{ready: true}
	orchestrator := newTestOrchestrator(t, remote, "add-order")
	orchestrator.config.UpstreamAddress = "new-versiond-3"
	if err := orchestrator.Add(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertCallOrder(
		t,
		remote.callLog(),
		"gonka-routerctl host add --operation-id add-order --address new-versiond-3 versiond-2",
		"docker update --restart=unless-stopped",
		"docker start",
		"127.0.0.1:8081/ready",
		"--from joining --to active --target active",
	)
	assertJournal(t, orchestrator.config.JournalPath, "add", "complete")
}

func TestAddRequiresExplicitDockerRestartPolicy(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "add-no-policy")
	orchestrator.config.DockerRestartPolicy = ""
	if err := orchestrator.Add(context.Background()); err == nil {
		t.Fatal("add without a restart policy succeeded")
	}
	if calls := remote.callLog(); strings.Contains(calls, "host add") {
		t.Fatalf("add changed router state before policy validation:\n%s", calls)
	}
}

func TestReplaceRetriesDedicatedReadinessProbe(t *testing.T) {
	remote := &fakeRemote{ready: true, readinessErrors: 1}
	orchestrator := newTestOrchestrator(t, remote, "replace-ready-retry")
	if err := orchestrator.Replace(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := remote.callLog(); strings.Count(calls, "127.0.0.1:8081/ready") < 2 {
		t.Fatalf("replacement did not retry readiness:\n%s", calls)
	}
}

func TestEvacuateUsesSIGKILLOnlyAfterGrace(t *testing.T) {
	remote := &fakeRemote{
		running: true,
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
	remote := &fakeRemote{ready: true}
	orchestrator := newTestOrchestrator(t, remote, "replace-order")
	orchestrator.config.UpstreamAddress = "replacement-2"
	if err := orchestrator.Replace(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := remote.callLog()
	assertCallOrder(t, calls,
		"--from offline --to joining --target active",
		"--address replacement-2",
		"docker update --restart=unless-stopped",
		"docker start",
		"127.0.0.1:8081/ready",
		"--from joining --to active --target active",
	)
}

func TestReplaceResumesFromEveryProvisionCheckpoint(t *testing.T) {
	tests := []struct {
		phase     string
		required  []string
		forbidden []string
	}{
		{
			phase: phaseMembershipLoaded,
			required: []string{
				"--from offline --to joining",
				"docker start",
				"127.0.0.1:8081/ready",
				"--from joining --to active --target active",
			},
			forbidden: []string{"gonka-routerctl status"},
		},
		{
			phase: phaseRuntimeValidated,
			required: []string{
				"--from offline --to joining",
				"docker start",
				"127.0.0.1:8081/ready",
				"--from joining --to active --target active",
			},
			forbidden: []string{"gonka-routerctl status"},
		},
		{
			phase: phaseRestartPolicyResolved,
			required: []string{
				"--from offline --to joining",
				"docker start",
				"127.0.0.1:8081/ready",
				"--from joining --to active --target active",
			},
			forbidden: []string{"gonka-routerctl status"},
		},
		{
			phase: "router_joining",
			required: []string{
				"docker start",
				"127.0.0.1:8081/ready",
				"--from joining --to active --target active",
			},
			forbidden: []string{"--from offline --to joining"},
		},
		{
			phase: "host_started",
			required: []string{
				"127.0.0.1:8081/ready",
				"--from joining --to active --target active",
			},
			forbidden: []string{
				"--from offline --to joining",
				"docker start",
			},
		},
		{
			phase:    "host_ready",
			required: []string{"--from joining --to active --target active"},
			forbidden: []string{
				"--from offline --to joining",
				"docker start",
				"127.0.0.1:8081/ready",
			},
		},
		{
			phase: "router_active",
			forbidden: []string{
				"--from offline --to joining",
				"docker start",
				"127.0.0.1:8081/ready",
				"--from joining --to active --target active",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			operationID := "replace-resume-" + strings.ReplaceAll(tt.phase, "_", "-")
			remote := &fakeRemote{ready: true, running: true}
			orchestrator := newTestOrchestrator(t, remote, operationID)
			if err := orchestrator.writeJournal(Journal{
				SchemaVersion:         hostctlJournalSchemaVersion,
				OperationID:           operationID,
				MembershipID:          "membership-versiond-2",
				Mode:                  "replace",
				Scope:                 orchestrator.operationScope(),
				Phase:                 tt.phase,
				PreviousRestartPolicy: "unless-stopped",
				UpdatedAt:             time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}

			if err := orchestrator.Replace(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls := remote.callLog()
			if len(tt.required) > 0 {
				assertCallOrder(t, calls, tt.required...)
			}
			for _, fragment := range tt.forbidden {
				if strings.Contains(calls, fragment) {
					t.Fatalf(
						"replace resumed from %s repeated %q:\n%s",
						tt.phase,
						fragment,
						calls,
					)
				}
			}
			assertJournal(t, orchestrator.config.JournalPath, "replace", "complete")
		})
	}
}

func TestReplaceRestoresPolicyFromEvacuationJournal(t *testing.T) {
	remote := &fakeRemote{ready: true}
	orchestrator := newTestOrchestrator(t, remote, "replace-policy")
	orchestrator.config.DockerRestartPolicy = ""
	orchestrator.config.EvacuationJournal = filepath.Join(t.TempDir(), "evacuation.json")
	evacuationScope := orchestrator.operationScope()
	evacuationScope.VersiondSSH = "retired-versiond-host"
	evacuation := Journal{
		SchemaVersion:         legacyHostctlJournalSchemaVersion,
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
		SchemaVersion:         hostctlJournalSchemaVersion,
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
	if calls := remote.callLog(); strings.Contains(calls, "--to joining") {
		t.Fatalf("replacement mutated router before journal validation:\n%s", calls)
	}
}

func TestReplaceRejectsPolicyFromPreviousMembership(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "replace-stale-membership")
	orchestrator.config.DockerRestartPolicy = ""
	orchestrator.config.EvacuationJournal = filepath.Join(t.TempDir(), "evacuation.json")
	evacuation := Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
		OperationID:           "old-membership",
		MembershipID:          "membership-retired-versiond-2",
		Mode:                  "evacuate",
		Scope:                 orchestrator.operationScope(),
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

	err = orchestrator.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "different router membership") {
		t.Fatalf("replace error = %v, want stale membership rejection", err)
	}
	if calls := remote.callLog(); strings.Contains(calls, "--to joining") {
		t.Fatalf("replacement mutated router with a stale journal:\n%s", calls)
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
	if calls := remote.callLog(); strings.Contains(calls, "--to joining") {
		t.Fatalf("replacement changed router state before policy validation:\n%s", calls)
	}
}

func TestCancelEvacuationRestoresPolicyBeforeReactivating(t *testing.T) {
	remote := &fakeRemote{running: true}
	orchestrator := newTestOrchestrator(t, remote, "cancel")
	journal := Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
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

	if err := orchestrator.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCallOrder(
		t,
		remote.callLog(),
		"docker update --restart=always",
		"gonka-routerctl host cancel",
	)
	assertJournalPhase(t, orchestrator.config.JournalPath, "canceled")
}

func TestCancelEvacuationResumesItsDurableCompensation(t *testing.T) {
	remote := &fakeRemote{running: true, activateErrors: 1}
	orchestrator := newTestOrchestrator(t, remote, "cancel-resume")
	journal := Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
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

	if err := orchestrator.Cancel(context.Background()); err == nil {
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

	if err := orchestrator.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "canceled")
}

func TestStoppedHostResumesForwardAfterInterruptedCancellation(t *testing.T) {
	for _, cancellationPhase := range []string{
		cancellationRequested,
		cancellationRestartRestored,
	} {
		t.Run(cancellationPhase, func(t *testing.T) {
			remote := &fakeRemote{}
			orchestrator := newTestOrchestrator(
				t,
				remote,
				"cancel-stopped-"+cancellationPhase,
			)
			journal := Journal{
				SchemaVersion:         hostctlJournalSchemaVersion,
				OperationID:           orchestrator.config.OperationID,
				Mode:                  "evacuate",
				Scope:                 orchestrator.operationScope(),
				Phase:                 phaseRestartDisabled,
				CancellationPhase:     cancellationPhase,
				PreviousRestartPolicy: "always",
				UpdatedAt:             time.Now().UTC(),
			}
			if err := orchestrator.writeJournal(journal); err != nil {
				t.Fatal(err)
			}

			err := orchestrator.Cancel(context.Background())
			if err == nil || !strings.Contains(err.Error(), "resume evacuate") {
				t.Fatalf("cancel error = %v, want forward-recovery guidance", err)
			}
			if err := orchestrator.Evacuate(context.Background()); err != nil {
				t.Fatal(err)
			}

			assertJournalPhase(t, orchestrator.config.JournalPath, phaseComplete)
			assertJournalCancellationPhase(t, orchestrator.config.JournalPath, "")
			calls := remote.callLog()
			assertCallOrder(
				t,
				calls,
				"docker update --restart=no",
				"--from draining --to stopping --target offline",
				"--from stopping --to offline --target offline",
			)
			if strings.Contains(calls, "gonka-routerctl host cancel") {
				t.Fatalf("forward recovery reactivated the router:\n%s", calls)
			}
			if strings.Contains(calls, "docker kill --signal TERM") {
				t.Fatalf("forward recovery signaled an already stopped host:\n%s", calls)
			}
		})
	}
}

func TestStoppedHostRecognizesCancellationCommittedBeforeCheckpoint(
	t *testing.T,
) {
	operationID := "cancel-committed-before-checkpoint"
	remote := &fakeRemote{
		routerOperation: &router.OperationLookup{
			OperationID: operationID,
			Completed:   true,
			Completion: &router.OperationCompletion{
				OperationID:  operationID,
				Action:       "cancel",
				Host:         "versiond-2",
				MembershipID: "membership-versiond-2",
				Target:       router.HostActive,
				Result:       "canceled",
				CompletedAt:  time.Now().UTC(),
			},
		},
	}
	orchestrator := newTestOrchestrator(t, remote, operationID)
	journal := Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
		OperationID:           operationID,
		Mode:                  "evacuate",
		Scope:                 orchestrator.operationScope(),
		Phase:                 phaseRestartDisabled,
		CancellationPhase:     cancellationRestartRestored,
		PreviousRestartPolicy: "always",
		UpdatedAt:             time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	err := orchestrator.Evacuate(context.Background())
	if err == nil ||
		!strings.Contains(err.Error(), "start a new evacuate operation") {
		t.Fatalf("evacuation error = %v, want terminal cancellation guidance", err)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseCanceled)
	assertJournalCancellationPhase(
		t,
		orchestrator.config.JournalPath,
		cancellationComplete,
	)
	calls := remote.callLog()
	if strings.Contains(calls, "--from draining --to stopping") {
		t.Fatalf("terminal canceled operation resumed its stop workflow:\n%s", calls)
	}
}

func TestEvacuationReassertsDisabledRestartPolicyBeforeSignal(t *testing.T) {
	remote := &fakeRemote{
		running:    true,
		stopOnTerm: true,
	}
	orchestrator := newTestOrchestrator(t, remote, "restart-reassert")
	journal := Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
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

func TestProbeFailureBeforeSignalIntentCanBeCanceled(t *testing.T) {
	remote := &fakeRemote{
		running:            true,
		runningProbeErrors: 1,
	}
	orchestrator := newTestOrchestrator(t, remote, "cancel-probe-failure")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion:         hostctlJournalSchemaVersion,
		OperationID:           orchestrator.config.OperationID,
		Mode:                  "evacuate",
		Scope:                 orchestrator.operationScope(),
		Phase:                 phaseRestartDisabled,
		PreviousRestartPolicy: "always",
		UpdatedAt:             time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.Evacuate(context.Background()); err == nil {
		t.Fatal("evacuation succeeded despite the injected process probe failure")
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, phaseRestartDisabled)
	if calls := remote.callLog(); strings.Contains(calls, "docker kill --signal TERM") {
		t.Fatalf("evacuation sent SIGTERM after a failed process probe:\n%s", calls)
	}

	if err := orchestrator.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertJournalPhase(t, orchestrator.config.JournalPath, "canceled")
}

func TestCancelEvacuationRejectsRequestedSignal(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "cancel-late")
	journal := Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   "cancel-late",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "term_requested",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Cancel(context.Background()); err == nil {
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

func TestWaitForStoppedAcceptsRemovedContainer(t *testing.T) {
	remote := &fakeRemote{runtimeAbsent: true}
	orchestrator := newTestOrchestrator(t, remote, "probe-absent")

	stopped, err := orchestrator.waitForStopped(
		context.Background(),
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("removed container was not accepted as stopped")
	}
}

func TestWaitForStoppedEscalatesAfterPersistentProbeFailure(t *testing.T) {
	remote := &fakeRemote{runningProbeErrors: 1000}
	orchestrator := newTestOrchestrator(t, remote, "probe-unavailable")

	stopped, err := orchestrator.waitForStopped(
		context.Background(),
		10*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Fatal("unobservable runtime was reported stopped")
	}
}

func TestWaitForStoppedBoundsBlockingProbeByStopDeadline(t *testing.T) {
	orchestrator := newTestOrchestrator(
		t,
		blockingRemote{},
		"probe-blocked",
	)
	started := time.Now()
	stopped, err := orchestrator.waitForStopped(
		context.Background(),
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Fatal("blocked runtime probe was reported stopped")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("blocked stop probe took %s, want bounded wait", elapsed)
	}
}

type blockingRemote struct{}

func (blockingRemote) Run(ctx context.Context, _ string, _ ...string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

type successfulAtDeadlineRemote struct{}

func (successfulAtDeadlineRemote) Run(
	ctx context.Context,
	_ string,
	_ ...string,
) (string, error) {
	<-ctx.Done()
	return "completed", nil
}

func TestRemoteCommandTimeout(t *testing.T) {
	orchestrator := newTestOrchestrator(t, blockingRemote{}, "command-timeout")
	orchestrator.config.CommandTimeout = 5 * time.Millisecond
	err := orchestrator.routerTransition(
		context.Background(),
		&Journal{},
		router.HostActive,
		router.HostDraining,
		router.HostOffline,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("remote command error = %v, want deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "exceeded 5ms") {
		t.Fatalf("remote command error = %v, want command timeout context", err)
	}
}

func TestRemoteCommandReportsParentCancellation(t *testing.T) {
	orchestrator := newTestOrchestrator(t, blockingRemote{}, "command-canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := orchestrator.runRemote(ctx, "router-host", "status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("remote command error = %v, want context canceled", err)
	}
	if strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("parent cancellation reported as command timeout: %v", err)
	}
}

func TestRemoteCommandAcceptsSuccessfulDeadlineResult(t *testing.T) {
	orchestrator := newTestOrchestrator(
		t,
		successfulAtDeadlineRemote{},
		"command-deadline-success",
	)
	orchestrator.config.CommandTimeout = 5 * time.Millisecond

	output, err := orchestrator.runRemote(
		context.Background(),
		"router-host",
		"status",
	)
	if err != nil {
		t.Fatal(err)
	}
	if output != "completed" {
		t.Fatalf("remote command output = %q, want completed", output)
	}
}

func TestEvacuateRejectsShortRuntimeStopContractBeforeRouterMutation(t *testing.T) {
	remote := &fakeRemote{running: true, stopTimeout: "10"}
	orchestrator := newTestOrchestrator(t, remote, "short-stop")
	orchestrator.config.KillGrace = 30 * time.Minute
	if err := orchestrator.Evacuate(context.Background()); err == nil {
		t.Fatal("evacuation accepted a ten-second Docker stop timeout")
	}
	if calls := remote.callLog(); strings.Contains(calls, "--to draining") {
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
				return "TimeoutStopUSec=30min\nKillMode=mixed\nSendSIGKILL=yes\n", nil
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
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "resume")
	journal := Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
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
	if strings.Contains(calls, "--to draining") || strings.Contains(calls, "docker kill") {
		t.Fatalf("resumed operation repeated completed phases:\n%s", calls)
	}
	if !strings.Contains(calls, "--from stopping --to offline") {
		t.Fatalf("resumed operation did not finish router offline phase:\n%s", calls)
	}
}

func TestEvacuateReplaysRouterStoppingWithoutRepeatingDrain(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "resume-router-stopping")
	journal := Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   "resume-router-stopping",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "term_requested",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := orchestrator.writeJournal(journal); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := remote.callLog()
	if strings.Contains(calls, "--from active --to draining") {
		t.Fatalf("resume repeated the already completed drain edge:\n%s", calls)
	}
	if !strings.Contains(calls, "--from draining --to stopping") {
		t.Fatalf("resume did not replay the stopping edge:\n%s", calls)
	}
}

func TestEvacuateResumesLegacyHostIdleCheckpoint(t *testing.T) {
	remote := &fakeRemote{running: true, stopOnTerm: true}
	orchestrator := newTestOrchestrator(t, remote, "resume-host-idle")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion: previousHostctlJournalSchemaVersion,
		OperationID:   "resume-host-idle",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         phaseLegacyHostIdle,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.Evacuate(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := remote.callLog()
	if strings.Contains(calls, "--from active --to draining") {
		t.Fatalf("legacy resume repeated the completed drain edge:\n%s", calls)
	}
	assertCallOrder(
		t,
		calls,
		"HostConfig.RestartPolicy",
		"docker update --restart=no",
		"--from draining --to stopping",
		"docker kill --signal TERM",
	)
}

func TestResumeRejectsChangedOperationScope(t *testing.T) {
	remote := &fakeRemote{}
	orchestrator := newTestOrchestrator(t, remote, "scope")
	journal := Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
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

func TestLoadExistingJournalMigratesSchemaTwoWorkflow(t *testing.T) {
	orchestrator := newTestOrchestrator(t, &fakeRemote{}, "schema-two")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion: previousHostctlJournalSchemaVersion,
		OperationID:   "schema-two",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         phaseRouterDraining,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	journal, err := orchestrator.loadExistingJournal("evacuate")
	if err != nil {
		t.Fatal(err)
	}
	if journal.SchemaVersion != hostctlJournalSchemaVersion {
		t.Fatalf(
			"migrated journal schema = %d, want %d",
			journal.SchemaVersion,
			hostctlJournalSchemaVersion,
		)
	}
	if journal.Phase != phaseRouterDraining {
		t.Fatalf("migrated journal phase = %q, want router_draining", journal.Phase)
	}
}

func TestLoadExistingJournalMigratesLegacyCanceledState(t *testing.T) {
	orchestrator := newTestOrchestrator(t, &fakeRemote{}, "legacy-canceled")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion: legacyHostctlJournalSchemaVersion,
		OperationID:   "legacy-canceled",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "canceled",
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	journal, err := orchestrator.loadExistingJournal("evacuate")
	if err != nil {
		t.Fatal(err)
	}
	if journal.SchemaVersion != hostctlJournalSchemaVersion {
		t.Fatalf(
			"migrated journal schema = %d, want %d",
			journal.SchemaVersion,
			hostctlJournalSchemaVersion,
		)
	}
	if journal.CancellationPhase != "complete" {
		t.Fatalf(
			"migrated cancellation phase = %q, want complete",
			journal.CancellationPhase,
		)
	}

	data, err := os.ReadFile(orchestrator.config.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Journal
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SchemaVersion != hostctlJournalSchemaVersion ||
		persisted.CancellationPhase != "complete" {
		t.Fatalf("persisted migrated journal = %#v", persisted)
	}
}

func TestLoadExistingJournalRejectsIncompleteCurrentCancellation(t *testing.T) {
	orchestrator := newTestOrchestrator(t, &fakeRemote{}, "invalid-canceled")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   "invalid-canceled",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "canceled",
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	_, err := orchestrator.loadExistingJournal("evacuate")
	if err == nil || !strings.Contains(err.Error(), "invalid evacuation cancellation phase") {
		t.Fatalf("load error = %v, want invalid cancellation state", err)
	}
}

func TestOperationLockReportsOwnerWithoutWaiting(t *testing.T) {
	orchestrator := newTestOrchestrator(t, &fakeRemote{}, "locked")
	if err := orchestrator.writeJournal(Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   "locked",
		Mode:          "evacuate",
		Scope:         orchestrator.operationScope(),
		Phase:         "router_draining",
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	release := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- orchestrator.withOperationLock(
			context.Background(),
			"evacuate",
			func() error {
				close(acquired)
				<-release
				return nil
			},
		)
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("operation owner did not acquire lock")
	}

	started := time.Now()
	err := orchestrator.withOperationLock(context.Background(), "cancel", func() error {
		t.Fatal("operation ran while its lock was held")
		return nil
	})
	if !errors.Is(err, errOperationBusy) {
		t.Fatalf("lock error = %v, want operation busy", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("busy lock returned after %s, want fail-fast", elapsed)
	}
	message := err.Error()
	for _, want := range []string{
		`owner_operation_id="locked"`,
		`owner_action="evacuate"`,
		"owner_pid=" + strconv.Itoa(os.Getpid()),
		`journal_phase="router_draining"`,
		"commands do not queue",
		"interrupt the owner",
		"term_requested",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("busy error %q does not contain %q", message, want)
		}
	}

	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
}

func TestOperationLockSerializesDifferentIDsForSameRouter(t *testing.T) {
	dir := t.TempDir()
	owner := newTestOrchestrator(t, &fakeRemote{}, "owner-operation")
	owner.config.StateDir = dir
	owner.config.JournalPath = filepath.Join(
		t.TempDir(),
		"owner-operation.json",
	)
	if err := owner.writeJournal(Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   owner.config.OperationID,
		Mode:          "evacuate",
		Scope:         owner.operationScope(),
		Phase:         phaseRouterDraining,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	contenderConfig := owner.config
	contenderConfig.OperationID = "contender-operation"
	contenderConfig.JournalPath = filepath.Join(
		t.TempDir(),
		"contender-operation.json",
	)
	contender, err := New(contenderConfig, &fakeRemote{})
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	release := make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- owner.withOperationLock(
			context.Background(),
			"evacuate",
			func() error {
				close(acquired)
				<-release
				return nil
			},
		)
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("operation owner did not acquire router-scoped lock")
	}

	err = contender.withOperationLock(
		context.Background(),
		"decommission",
		func() error {
			t.Fatal("different operation ID bypassed router-scoped lock")
			return nil
		},
	)
	if !errors.Is(err, errOperationBusy) {
		t.Fatalf("lock error = %v, want operation busy", err)
	}
	for _, want := range []string{
		`requested_operation_id="contender-operation"`,
		`owner_operation_id="owner-operation"`,
		`owner_upstream="versiond-2"`,
		`journal_phase="router_draining"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("busy error %q does not contain %q", err, want)
		}
	}

	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
}

func TestOperationLockHonorsCanceledContext(t *testing.T) {
	orchestrator := newTestOrchestrator(t, &fakeRemote{}, "lock-canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := orchestrator.withOperationLock(ctx, "evacuate", func() error {
		t.Fatal("operation ran with canceled context")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lock error = %v, want context canceled", err)
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
		ReadyTimeout:        50 * time.Millisecond,
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

func assertJournal(t *testing.T, path, wantMode, wantPhase string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Mode != wantMode || journal.Phase != wantPhase {
		t.Fatalf(
			"journal mode/phase = %q/%q, want %q/%q",
			journal.Mode,
			journal.Phase,
			wantMode,
			wantPhase,
		)
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
