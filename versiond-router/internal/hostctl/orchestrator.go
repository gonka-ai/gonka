package hostctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"versiond-router/internal/router"
)

type Runtime string

const (
	RuntimeDocker                       Runtime = "docker"
	RuntimeSystemd                      Runtime = "systemd"
	legacyHostctlJournalSchemaVersion           = 1
	previousHostctlJournalSchemaVersion         = 2
	hostctlJournalSchemaVersion                 = 3
)

type Config struct {
	RouterSSH           string
	RouterRuntime       Runtime
	RouterService       string
	Upstream            string
	UpstreamAddress     string
	LegacyHost          string
	VersiondSSH         string
	VersiondRuntime     Runtime
	VersiondService     string
	OperationID         string
	JournalPath         string
	EvacuationJournal   string
	ReadyTimeout        time.Duration
	PollInterval        time.Duration
	KillGrace           time.Duration
	CommandTimeout      time.Duration
	ReadinessURL        string
	DockerRestartPolicy string
	ForceRouterGuard    bool
}

type OperationScope struct {
	RouterSSH           string  `json:"router_ssh"`
	RouterRuntime       Runtime `json:"router_runtime"`
	RouterService       string  `json:"router_service"`
	Upstream            string  `json:"upstream"`
	UpstreamAddress     string  `json:"upstream_address,omitempty"`
	LegacyHost          string  `json:"legacy_host,omitempty"`
	VersiondSSH         string  `json:"versiond_ssh"`
	VersiondRuntime     Runtime `json:"versiond_runtime"`
	VersiondService     string  `json:"versiond_service"`
	ReadinessURL        string  `json:"readiness_url"`
	EvacuationJournal   string  `json:"evacuation_journal,omitempty"`
	DockerRestartPolicy string  `json:"docker_restart_policy,omitempty"`
}

type Journal struct {
	SchemaVersion         int            `json:"schema_version"`
	OperationID           string         `json:"operation_id"`
	MembershipID          string         `json:"membership_id,omitempty"`
	Mode                  string         `json:"mode"`
	Scope                 OperationScope `json:"scope"`
	Phase                 string         `json:"phase"`
	CancellationPhase     string         `json:"cancellation_phase,omitempty"`
	PreviousRestartPolicy string         `json:"previous_restart_policy,omitempty"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

var errOperationBusy = errors.New("host operation is already running")

type operationLockOwner struct {
	OperationID string    `json:"operation_id"`
	Action      string    `json:"action"`
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
}

type Orchestrator struct {
	config          Config
	remote          Remote
	versiondRuntime serviceRuntime
}

func New(config Config, remote Remote) (*Orchestrator, error) {
	if config.OperationID == "" {
		return nil, errors.New("operation id is required")
	}
	if config.RouterSSH == "" || config.VersiondSSH == "" {
		return nil, errors.New("router and versiond SSH destinations are required")
	}
	if config.RouterService == "" || config.VersiondService == "" || config.Upstream == "" {
		return nil, errors.New("router service, versiond service, and upstream are required")
	}
	if !validRuntime(config.RouterRuntime) || !validRuntime(config.VersiondRuntime) {
		return nil, errors.New("runtime must be docker or systemd")
	}
	if config.ReadyTimeout <= 0 || config.PollInterval <= 0 || config.KillGrace <= 0 {
		return nil, errors.New("ready timeout, poll interval, and kill grace must be positive")
	}
	if config.CommandTimeout <= 0 {
		config.CommandTimeout = 30 * time.Second
	}
	if config.ReadinessURL == "" {
		config.ReadinessURL = "http://127.0.0.1:8080/ready"
	}
	parsedReadinessURL, err := url.Parse(config.ReadinessURL)
	if err != nil || parsedReadinessURL.Host == "" ||
		(parsedReadinessURL.Scheme != "http" && parsedReadinessURL.Scheme != "https") {
		return nil, fmt.Errorf("invalid versiond readiness URL %q", config.ReadinessURL)
	}
	if config.DockerRestartPolicy != "" {
		if err := validateDockerRestartPolicy(config.DockerRestartPolicy); err != nil {
			return nil, err
		}
	}
	if config.JournalPath == "" {
		return nil, errors.New("journal path is required")
	}
	if remote == nil {
		remote = SSHRemote{}
	}
	orchestrator := &Orchestrator{config: config, remote: remote}
	orchestrator.versiondRuntime = newServiceRuntime(
		config.VersiondRuntime,
		config.VersiondService,
		func(ctx context.Context, args ...string) (string, error) {
			return orchestrator.runRemote(ctx, config.VersiondSSH, args...)
		},
	)
	return orchestrator, nil
}

func validRuntime(runtime Runtime) bool {
	return runtime == RuntimeDocker || runtime == RuntimeSystemd
}

func (o *Orchestrator) Evacuate(ctx context.Context) error {
	return o.withOperationLock(ctx, "evacuate", func() error {
		return o.stopHost(ctx, "evacuate")
	})
}

func (o *Orchestrator) Decommission(ctx context.Context) error {
	return o.withOperationLock(ctx, "decommission", func() error {
		return o.stopHost(ctx, "decommission")
	})
}

func (o *Orchestrator) stopHost(ctx context.Context, mode string) error {
	workflow := stopWorkflow(mode)
	if workflow == nil {
		return fmt.Errorf("unsupported stop mode %q", mode)
	}
	journal, err := o.loadOrCreateJournal(mode)
	if err != nil {
		return err
	}
	if journal.Phase == phaseCanceled {
		return fmt.Errorf("%s was canceled; start a new operation", mode)
	}
	if journal.CancellationPhase != "" {
		resumed, err := o.resumeStopAfterFailedCancellation(ctx, &journal)
		if err != nil {
			return err
		}
		if !resumed {
			return fmt.Errorf("%s cancellation is in progress; resume cancel", mode)
		}
	}
	if err := o.prepareStopWorkflow(ctx, mode, &journal); err != nil {
		return err
	}
	return o.runWorkflow(ctx, &journal, workflow)
}

func (o *Orchestrator) Cancel(ctx context.Context) error {
	return o.withOperationLock(ctx, "cancel", func() error {
		journal, err := o.loadExistingStopJournal()
		if err != nil {
			return err
		}
		if journal.Phase == phaseCanceled {
			return nil
		}
		return o.runWorkflow(ctx, &journal, &cancellationWorkflow)
	})
}

func (o *Orchestrator) Replace(ctx context.Context) error {
	return o.withOperationLock(ctx, "replace", func() error {
		return o.provision(ctx, "replace")
	})
}

func (o *Orchestrator) Add(ctx context.Context) error {
	return o.withOperationLock(ctx, "add", func() error {
		return o.provision(ctx, "add")
	})
}

func (o *Orchestrator) provision(ctx context.Context, mode string) error {
	workflow := provisionWorkflow(mode)
	if workflow == nil {
		return fmt.Errorf("unsupported provision mode %q", mode)
	}
	journal, err := o.loadOrCreateJournal(mode)
	if err != nil {
		return err
	}
	return o.runWorkflow(ctx, &journal, workflow)
}

func stopTarget(mode string) router.HostState {
	if mode == "decommission" {
		return router.HostRemoved
	}
	return router.HostOffline
}

func (o *Orchestrator) routerTransition(
	ctx context.Context,
	journal *Journal,
	from router.HostState,
	to router.HostState,
	target router.HostState,
) error {
	if to == router.HostRemoved && journal.MembershipID == "" {
		if err := o.loadRouterMembership(ctx, journal); err != nil {
			return err
		}
	}
	args := []string{
		"gonka-routerctl", "host", "transfer",
		"--operation-id", o.config.OperationID,
		"--from", string(from),
		"--to", string(to),
		"--target", string(target),
	}
	if journal.MembershipID != "" {
		args = append(args, "--membership-id", journal.MembershipID)
	}
	if o.config.ForceRouterGuard &&
		(target == router.HostOffline || target == router.HostRemoved) {
		args = append(args, "--force")
	}
	if target == router.HostActive && o.config.UpstreamAddress != "" {
		args = append(args, "--address", o.config.UpstreamAddress)
	}
	if target == router.HostRemoved && o.config.LegacyHost != "" {
		args = append(args, "--legacy-host", o.config.LegacyHost)
	}
	args = append(args, o.config.Upstream)
	state, err := o.runRouterMutation(ctx, args)
	if err != nil {
		return err
	}
	return o.captureMembership(journal, state, to != router.HostRemoved)
}

func (o *Orchestrator) loadRouterMembership(
	ctx context.Context,
	journal *Journal,
) error {
	state, err := o.runRouterMutation(ctx, []string{"gonka-routerctl", "status"})
	if err != nil {
		return fmt.Errorf("read router membership: %w", err)
	}
	return o.captureMembership(journal, state, true)
}

func (o *Orchestrator) prepareStopWorkflow(
	ctx context.Context,
	mode string,
	journal *Journal,
) error {
	if journal.Phase != phaseStarted {
		return nil
	}
	state, err := o.runRouterMutation(ctx, []string{"gonka-routerctl", "status"})
	if err != nil {
		return fmt.Errorf("read router state before %s: %w", mode, err)
	}
	var target *router.Host
	for i := range state.Hosts {
		if state.Hosts[i].Name == o.config.Upstream {
			target = &state.Hosts[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("router response has no membership for host %s", o.config.Upstream)
	}
	if journal.MembershipID != "" && journal.MembershipID != target.MembershipID {
		return fmt.Errorf(
			"router membership changed for %s: journal has %s, router has %s",
			target.Name,
			journal.MembershipID,
			target.MembershipID,
		)
	}
	if err := validateStopTransfer(state, *target, o.config.OperationID, stopTarget(mode)); err != nil {
		return err
	}

	switch target.State {
	case router.HostActive, router.HostDraining:
		journal.MembershipID = target.MembershipID
		return o.writeJournal(*journal)
	case router.HostOffline:
		journal.MembershipID = target.MembershipID
		return o.adoptOfflineStop(ctx, mode, journal)
	default:
		return fmt.Errorf(
			"cannot %s host %s from router state %s",
			mode,
			target.Name,
			target.State,
		)
	}
}

func validateStopTransfer(
	state router.State,
	host router.Host,
	operationID string,
	target router.HostState,
) error {
	transfer := state.ActiveTransfer
	if transfer == nil {
		return nil
	}
	if transfer.ID != operationID {
		return fmt.Errorf(
			"%w: transfer %s owns membership %s (%s)",
			router.ErrHostOperation,
			transfer.ID,
			transfer.MembershipID,
			transfer.Host,
		)
	}
	if transfer.Host != host.Name ||
		transfer.MembershipID != host.MembershipID ||
		transfer.From != router.HostActive ||
		transfer.To != target {
		return fmt.Errorf(
			"%w: transfer %s does not match %s membership %s target %s",
			router.ErrOperationOwner,
			transfer.ID,
			host.Name,
			host.MembershipID,
			target,
		)
	}
	return nil
}

func (o *Orchestrator) adoptOfflineStop(
	ctx context.Context,
	mode string,
	journal *Journal,
) error {
	runtimeState, err := o.versiondServiceState(ctx)
	if err != nil {
		return fmt.Errorf("check offline versiond host: %w", err)
	}
	if runtimeState == serviceRunning {
		return errors.New(
			"offline versiond host is still running; stop it before continuing",
		)
	}
	runtimeState, err = o.reconcileDockerRestartDisabled(ctx, runtimeState)
	if err != nil {
		return fmt.Errorf(
			"disable Docker restart policy for offline host: %w",
			err,
		)
	}
	if runtimeState == serviceRunning {
		return errors.New(
			"offline versiond host restarted while adopting stop state",
		)
	}
	if runtimeState == serviceAbsent {
		slog.Warn(
			"adopting offline router state without a local versiond service",
			"operation_id", journal.OperationID,
			"mode", mode,
			"service", o.config.VersiondService,
		)
	}
	journal.Phase = phaseRouterOffline
	journal.UpdatedAt = time.Now().UTC()
	if err := o.writeJournal(*journal); err != nil {
		journal.Phase = phaseStarted
		return fmt.Errorf("checkpoint offline %s entry: %w", mode, err)
	}
	return nil
}

func (o *Orchestrator) routerAdd(ctx context.Context, journal *Journal) error {
	args := []string{
		"gonka-routerctl", "host", "add",
		"--operation-id", o.config.OperationID,
	}
	if journal.MembershipID != "" {
		args = append(args, "--membership-id", journal.MembershipID)
	}
	if o.config.UpstreamAddress != "" {
		args = append(args, "--address", o.config.UpstreamAddress)
	}
	args = append(args, o.config.Upstream)
	state, err := o.runRouterMutation(ctx, args)
	if err != nil {
		return err
	}
	return o.captureMembership(journal, state, true)
}

func (o *Orchestrator) routerCancel(ctx context.Context, journal *Journal) error {
	args := []string{
		"gonka-routerctl", "host", "cancel",
		"--operation-id", o.config.OperationID,
	}
	if journal.MembershipID != "" {
		args = append(args, "--membership-id", journal.MembershipID)
	}
	args = append(args, o.config.Upstream)
	state, err := o.runRouterMutation(ctx, args)
	if err != nil {
		return err
	}
	return o.captureMembership(journal, state, true)
}

func (o *Orchestrator) runRouterMutation(
	ctx context.Context,
	args []string,
) (router.State, error) {
	if o.config.RouterRuntime == RuntimeDocker {
		args = append([]string{"docker", "exec", o.config.RouterService}, args...)
	}
	output, err := o.runRemote(ctx, o.config.RouterSSH, args...)
	if err != nil {
		return router.State{}, err
	}
	var state router.State
	if err := json.Unmarshal([]byte(output), &state); err != nil {
		return router.State{}, fmt.Errorf("decode router state after transition: %w", err)
	}
	if err := state.Validate(); err != nil {
		return router.State{}, fmt.Errorf("validate router state after transition: %w", err)
	}
	return state, nil
}

func (o *Orchestrator) captureMembership(
	journal *Journal,
	state router.State,
	requireHost bool,
) error {
	for _, host := range state.Hosts {
		if host.Name != o.config.Upstream {
			continue
		}
		if journal.MembershipID != "" && journal.MembershipID != host.MembershipID {
			return fmt.Errorf(
				"router membership changed for %s: journal has %s, router has %s",
				host.Name,
				journal.MembershipID,
				host.MembershipID,
			)
		}
		if journal.MembershipID == host.MembershipID {
			return nil
		}
		journal.MembershipID = host.MembershipID
		return o.writeJournal(*journal)
	}
	if requireHost {
		return fmt.Errorf(
			"router response has no membership for host %s",
			o.config.Upstream,
		)
	}
	if journal.MembershipID == "" {
		return fmt.Errorf(
			"removed host %s has no membership recorded in the operation journal",
			o.config.Upstream,
		)
	}
	return nil
}

func (o *Orchestrator) probeReadiness(ctx context.Context) error {
	timeoutSeconds := int((o.config.CommandTimeout + time.Second - 1) / time.Second)
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}
	var args []string
	if o.config.VersiondRuntime == RuntimeDocker {
		args = []string{
			"docker", "exec", o.config.VersiondService,
			"wget", "-q", "-T", strconv.Itoa(timeoutSeconds), "-O", "/dev/null", o.config.ReadinessURL,
		}
	} else {
		args = []string{
			"curl", "--fail", "--silent", "--show-error",
			"--output", "/dev/null",
			"--max-time", strconv.Itoa(timeoutSeconds), o.config.ReadinessURL,
		}
	}
	_, err := o.runRemote(ctx, o.config.VersiondSSH, args...)
	return err
}

func (o *Orchestrator) waitForReady(ctx context.Context) error {
	deadline := time.Now().Add(o.config.ReadyTimeout)
	var lastErr error
	for {
		err := o.probeReadiness(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("versiond readiness timeout: %w", lastErr)
			}
			return errors.New("versiond readiness timeout")
		}
		if err := wait(ctx, o.config.PollInterval); err != nil {
			return err
		}
	}
}

func (o *Orchestrator) signalVersiond(ctx context.Context, signal string) error {
	return o.versiondRuntime.Signal(ctx, signal)
}

func (o *Orchestrator) startVersiond(ctx context.Context, restartPolicy string) error {
	return o.versiondRuntime.Start(ctx, restartPolicy)
}

func (o *Orchestrator) dockerRestartPolicy(ctx context.Context) (string, error) {
	return o.versiondRuntime.RestartPolicy(ctx)
}

func (o *Orchestrator) setDockerRestartPolicy(ctx context.Context, policy string) error {
	return o.versiondRuntime.SetRestartPolicy(ctx, policy)
}

func (o *Orchestrator) versiondServiceState(ctx context.Context) (serviceState, error) {
	return o.versiondRuntime.State(ctx)
}

func (o *Orchestrator) versiondRunning(ctx context.Context) (bool, error) {
	state, err := o.versiondServiceState(ctx)
	return state == serviceRunning, err
}

func (o *Orchestrator) waitForStopped(ctx context.Context, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		running, err := o.versiondRunning(ctx)
		if err == nil && !running {
			return true, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return false, fmt.Errorf(
					"versiond status unavailable until stop timeout: %w",
					lastErr,
				)
			}
			return false, nil
		}
		if err := wait(ctx, o.config.PollInterval); err != nil {
			return false, err
		}
	}
}

func (o *Orchestrator) runRemote(
	ctx context.Context,
	destination string,
	args ...string,
) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, o.config.CommandTimeout)
	defer cancel()
	output, err := o.remote.Run(commandCtx, destination, args...)
	if err == nil {
		return output, nil
	}
	if ctx.Err() != nil {
		return output, fmt.Errorf(
			"remote command on %s canceled: %w",
			destination,
			ctx.Err(),
		)
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return output, fmt.Errorf(
			"remote command on %s exceeded %s: %w",
			destination,
			o.config.CommandTimeout,
			commandCtx.Err(),
		)
	}
	return output, err
}

func validateDockerRestartPolicy(policy string) error {
	switch policy {
	case "no", "always", "unless-stopped", "on-failure":
		return nil
	}
	prefix, retries, found := strings.Cut(policy, ":")
	if !found || prefix != "on-failure" {
		return fmt.Errorf("unsupported Docker restart policy %q", policy)
	}
	count, err := strconv.Atoi(retries)
	if err != nil || count <= 0 {
		return fmt.Errorf("invalid Docker restart policy %q", policy)
	}
	return nil
}

func (o *Orchestrator) replacementRestartPolicy(membershipID string) (string, error) {
	if o.config.DockerRestartPolicy != "" {
		return o.config.DockerRestartPolicy, nil
	}
	if o.config.EvacuationJournal == "" {
		return "", errors.New(
			"replacement requires --docker-restart-policy or --evacuation-journal",
		)
	}
	data, err := os.ReadFile(o.config.EvacuationJournal)
	if err != nil {
		return "", fmt.Errorf("read evacuation journal: %w", err)
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return "", fmt.Errorf("decode evacuation journal: %w", err)
	}
	journal, _, err = migrateJournal(journal)
	if err != nil || journal.Mode != "evacuate" {
		return "", errors.New("restart-policy source is not an evacuation journal")
	}
	validEvacuationPhase := evacuationWorkflow.hasState(journal.Phase) ||
		journal.Phase == phaseCanceled
	if !validEvacuationPhase || !validCancellationState(journal) {
		return "", errors.New("restart-policy source has an invalid evacuation state")
	}
	if !evacuationWorkflow.reached(journal.Phase, phaseRouterOffline) {
		return "", fmt.Errorf(
			"evacuation journal has not reached router_offline: %s",
			journal.Phase,
		)
	}
	if !sameLogicalVersiondHost(journal.Scope, o.operationScope()) {
		return "", errors.New("evacuation journal belongs to a different logical versiond host")
	}
	if journal.MembershipID != "" &&
		membershipID != "" &&
		journal.MembershipID != membershipID {
		return "", errors.New("evacuation journal belongs to a different router membership")
	}
	if err := validateDockerRestartPolicy(journal.PreviousRestartPolicy); err != nil {
		return "", fmt.Errorf("evacuation journal restart policy: %w", err)
	}
	return journal.PreviousRestartPolicy, nil
}

func (o *Orchestrator) provisionRestartPolicy(
	mode string,
	membershipID string,
) (string, error) {
	if mode == "add" {
		if o.config.DockerRestartPolicy == "" {
			return "", errors.New("add requires --docker-restart-policy")
		}
		return o.config.DockerRestartPolicy, nil
	}
	if mode != "replace" {
		return "", fmt.Errorf("unsupported provision mode %q", mode)
	}
	return o.replacementRestartPolicy(membershipID)
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (o *Orchestrator) loadExistingJournal(mode string) (Journal, error) {
	data, err := os.ReadFile(o.config.JournalPath)
	if err != nil {
		return Journal{}, err
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return Journal{}, err
	}
	journal, migrated, err := migrateJournal(journal)
	if err != nil {
		return Journal{}, err
	}
	if journal.OperationID != o.config.OperationID || journal.Mode != mode {
		return Journal{}, fmt.Errorf("journal belongs to operation %s/%s", journal.OperationID, journal.Mode)
	}
	if journal.Scope != o.operationScope() {
		return Journal{}, errors.New("journal target does not match the requested operation scope")
	}
	if !validJournalPhase(journal.Mode, journal.Phase) {
		return Journal{}, fmt.Errorf("invalid %s journal phase %q", journal.Mode, journal.Phase)
	}
	if !validCancellationState(journal) {
		return Journal{}, fmt.Errorf(
			"invalid evacuation cancellation phase %q at %q",
			journal.CancellationPhase,
			journal.Phase,
		)
	}
	if migrated {
		if err := o.writeJournal(journal); err != nil {
			return Journal{}, fmt.Errorf("persist migrated hostctl journal: %w", err)
		}
	}
	return journal, nil
}

func (o *Orchestrator) loadExistingStopJournal() (Journal, error) {
	data, err := os.ReadFile(o.config.JournalPath)
	if err != nil {
		return Journal{}, err
	}
	var identity struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return Journal{}, err
	}
	if stopWorkflow(identity.Mode) == nil {
		return Journal{}, fmt.Errorf("journal mode %q cannot be canceled", identity.Mode)
	}
	return o.loadExistingJournal(identity.Mode)
}

func (o *Orchestrator) loadOrCreateJournal(mode string) (Journal, error) {
	journal, err := o.loadExistingJournal(mode)
	if err == nil {
		return journal, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Journal{}, err
	}
	journal = Journal{
		SchemaVersion: hostctlJournalSchemaVersion,
		OperationID:   o.config.OperationID,
		Mode:          mode,
		Scope:         o.operationScope(),
		Phase:         phaseStarted,
		UpdatedAt:     time.Now().UTC(),
	}
	return journal, o.writeJournal(journal)
}

func migrateJournal(journal Journal) (Journal, bool, error) {
	switch journal.SchemaVersion {
	case hostctlJournalSchemaVersion:
		return journal, false, nil
	case legacyHostctlJournalSchemaVersion,
		previousHostctlJournalSchemaVersion:
		if stopWorkflow(journal.Mode) != nil &&
			journal.Phase == phaseCanceled &&
			journal.CancellationPhase == "" {
			journal.CancellationPhase = cancellationComplete
		}
		journal.SchemaVersion = hostctlJournalSchemaVersion
		return journal, true, nil
	default:
		return Journal{}, false, fmt.Errorf(
			"unsupported hostctl journal schema %d",
			journal.SchemaVersion,
		)
	}
}

func validJournalPhase(mode, phase string) bool {
	if stopWorkflow(mode) != nil && phase == phaseCanceled {
		return true
	}
	workflow := workflowForMode(mode)
	if workflow == nil {
		return false
	}
	return workflow.hasState(phase)
}

func validCancellationState(journal Journal) bool {
	workflow := stopWorkflow(journal.Mode)
	if workflow == nil {
		return journal.CancellationPhase == ""
	}
	if journal.CancellationPhase == "" {
		return journal.Phase != phaseCanceled
	}
	if !cancellationWorkflow.hasState(journal.CancellationPhase) ||
		journal.CancellationPhase == cancellationNotRequested {
		return false
	}
	if journal.CancellationPhase == cancellationComplete {
		return journal.Phase == phaseCanceled
	}
	return workflow.before(journal.Phase, phaseTermRequested)
}

func sameLogicalVersiondHost(left, right OperationScope) bool {
	return left.RouterSSH == right.RouterSSH &&
		left.RouterRuntime == right.RouterRuntime &&
		left.RouterService == right.RouterService &&
		left.Upstream == right.Upstream &&
		left.VersiondRuntime == right.VersiondRuntime &&
		left.VersiondService == right.VersiondService
}

func (o *Orchestrator) operationScope() OperationScope {
	return OperationScope{
		RouterSSH:           o.config.RouterSSH,
		RouterRuntime:       o.config.RouterRuntime,
		RouterService:       o.config.RouterService,
		Upstream:            o.config.Upstream,
		UpstreamAddress:     o.config.UpstreamAddress,
		LegacyHost:          o.config.LegacyHost,
		VersiondSSH:         o.config.VersiondSSH,
		VersiondRuntime:     o.config.VersiondRuntime,
		VersiondService:     o.config.VersiondService,
		ReadinessURL:        o.config.ReadinessURL,
		EvacuationJournal:   o.config.EvacuationJournal,
		DockerRestartPolicy: o.config.DockerRestartPolicy,
	}
}

func (o *Orchestrator) withOperationLock(
	ctx context.Context,
	action string,
	fn func() error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockPath := o.config.JournalPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return o.operationBusyError(lockPath, action)
		}
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := writeOperationLockOwner(lock, operationLockOwner{
		OperationID: o.config.OperationID,
		Action:      action,
		PID:         os.Getpid(),
		StartedAt:   time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("write operation lock owner: %w", err)
	}
	return fn()
}

func (o *Orchestrator) operationBusyError(lockPath, requestedAction string) error {
	details := []string{"operation_id=" + strconv.Quote(o.config.OperationID)}
	if owner, err := readOperationLockOwner(lockPath); err == nil {
		details = append(
			details,
			"owner_action="+strconv.Quote(owner.Action),
			"owner_pid="+strconv.Itoa(owner.PID),
			"owner_since="+owner.StartedAt.Format(time.RFC3339),
		)
	}
	mode, phase := readOperationStatus(o.config.JournalPath)
	if phase != "" {
		details = append(details, "journal_phase="+strconv.Quote(phase))
	}

	guidance := "wait for or interrupt the owner, then retry " + requestedAction
	if requestedAction == "cancel" {
		resumeAction := mode
		if stopWorkflow(resumeAction) == nil {
			resumeAction = "the stop operation"
		}
		guidance += "; cancellation is valid only before term_requested, " +
			"otherwise resume " + resumeAction
	}
	return fmt.Errorf(
		"%w: %s; commands do not queue behind a live owner; %s",
		errOperationBusy,
		strings.Join(details, " "),
		guidance,
	)
}

func writeOperationLockOwner(lock *os.File, owner operationLockOwner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := lock.Truncate(0); err != nil {
		return err
	}
	if _, err := lock.Seek(0, 0); err != nil {
		return err
	}
	if _, err := lock.Write(data); err != nil {
		return err
	}
	return lock.Sync()
}

func readOperationLockOwner(path string) (operationLockOwner, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return operationLockOwner{}, err
	}
	var owner operationLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return operationLockOwner{}, err
	}
	if owner.Action == "" || owner.PID <= 0 {
		return operationLockOwner{}, errors.New("operation lock owner is incomplete")
	}
	return owner, nil
}

func readOperationStatus(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var journal struct {
		Mode  string `json:"mode"`
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		return "", ""
	}
	return journal.Mode, journal.Phase
}

func (o *Orchestrator) writeJournal(journal Journal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(o.config.JournalPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hostctl-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, o.config.JournalPath); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}
