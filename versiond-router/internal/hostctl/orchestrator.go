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
)

type Runtime string

const (
	RuntimeDocker  Runtime = "docker"
	RuntimeSystemd Runtime = "systemd"
)

type Config struct {
	RouterSSH           string
	RouterRuntime       Runtime
	RouterService       string
	Upstream            string
	UpstreamAddress     string
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
		return o.evacuate(ctx)
	})
}

func (o *Orchestrator) evacuate(ctx context.Context) error {
	journal, err := o.loadOrCreateJournal("evacuate")
	if err != nil {
		return err
	}
	if journal.Phase == "canceled" {
		return errors.New("evacuation was canceled; start a new operation")
	}
	if journal.CancellationPhase != "" {
		return errors.New("evacuation cancellation is in progress; resume cancel")
	}
	if before(journal.Phase, "term_requested", evacuationPhases) {
		if err := o.versiondRuntime.ValidateStopContract(ctx, o.config.KillGrace); err != nil {
			return fmt.Errorf("versiond runtime preflight: %w", err)
		}
	}
	if before(journal.Phase, "router_draining", evacuationPhases) {
		if err := o.routerTransition(ctx, "drain"); err != nil {
			return err
		}
		if err := o.advance(&journal, "router_draining"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "term_sent", evacuationPhases) {
		// Reconfirm the traffic barrier before the first irreversible action.
		if err := o.routerTransition(ctx, "drain"); err != nil {
			return fmt.Errorf("reconfirm router drain before SIGTERM: %w", err)
		}
		if o.config.VersiondRuntime == RuntimeDocker {
			if journal.PreviousRestartPolicy == "" {
				policy, err := o.dockerRestartPolicy(ctx)
				if err != nil {
					return err
				}
				journal.PreviousRestartPolicy = policy
				if err := o.writeJournal(journal); err != nil {
					return err
				}
			}
			if err := o.setDockerRestartPolicy(ctx, "no"); err != nil {
				return err
			}
			if before(journal.Phase, "restart_disabled", evacuationPhases) {
				if err := o.advance(&journal, "restart_disabled"); err != nil {
					return err
				}
			}
		}
		running, err := o.versiondRunning(ctx)
		if err != nil {
			return err
		}
		if before(journal.Phase, "term_requested", evacuationPhases) {
			if err := o.advance(&journal, "term_requested"); err != nil {
				return err
			}
		}
		if running {
			if err := o.signalVersiond(ctx, "TERM"); err != nil {
				return err
			}
		}
		if err := o.advance(&journal, "term_sent"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "host_stopped", evacuationPhases) {
		stopped, err := o.waitForStopped(ctx, o.config.KillGrace)
		if err != nil {
			return err
		}
		if !stopped {
			slog.Warn("versiond kill grace expired; sending SIGKILL", "service", o.config.VersiondService)
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
		}
		if err := o.advance(&journal, "host_stopped"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "router_offline", evacuationPhases) {
		if err := o.routerTransition(ctx, "offline"); err != nil {
			return err
		}
		if err := o.advance(&journal, "router_offline"); err != nil {
			return err
		}
	}
	return o.advance(&journal, "complete")
}

func (o *Orchestrator) CancelEvacuation(ctx context.Context) error {
	return o.withOperationLock(ctx, "cancel", func() error {
		journal, err := o.loadExistingJournal("evacuate")
		if err != nil {
			return err
		}
		if journal.Phase == "canceled" {
			return nil
		}
		if journal.CancellationPhase == "" {
			if !before(journal.Phase, "term_requested", evacuationPhases) {
				return fmt.Errorf(
					"cannot cancel evacuation at or after signal intent (%s); resume it instead",
					journal.Phase,
				)
			}
			running, err := o.versiondRunning(ctx)
			if err != nil {
				return err
			}
			if !running {
				return errors.New("cannot reactivate a stopped versiond host")
			}
			if err := o.advanceCancellation(&journal, "requested"); err != nil {
				return err
			}
		}

		running, err := o.versiondRunning(ctx)
		if err != nil {
			return err
		}
		if !running {
			return errors.New("cannot reactivate a stopped versiond host")
		}
		if before(journal.CancellationPhase, "restart_restored", cancellationPhases) {
			if o.config.VersiondRuntime == RuntimeDocker && journal.PreviousRestartPolicy != "" {
				if err := o.setDockerRestartPolicy(ctx, journal.PreviousRestartPolicy); err != nil {
					return fmt.Errorf("restore Docker restart policy: %w", err)
				}
			}
			if err := o.advanceCancellation(&journal, "restart_restored"); err != nil {
				return err
			}
		}
		if before(journal.CancellationPhase, "router_active", cancellationPhases) {
			if err := o.routerTransition(ctx, "activate"); err != nil {
				return err
			}
			if err := o.advanceCancellation(&journal, "router_active"); err != nil {
				return err
			}
		}
		journal.Phase = "canceled"
		journal.CancellationPhase = "complete"
		journal.UpdatedAt = time.Now().UTC()
		return o.writeJournal(journal)
	})
}

func (o *Orchestrator) Replace(ctx context.Context) error {
	return o.withOperationLock(ctx, "replace", func() error {
		return o.replace(ctx)
	})
}

func (o *Orchestrator) replace(ctx context.Context) error {
	journal, err := o.loadOrCreateJournal("replace")
	if err != nil {
		return err
	}
	if before(journal.Phase, "host_started", replacementPhases) {
		if err := o.versiondRuntime.ValidateStopContract(ctx, o.config.KillGrace); err != nil {
			return fmt.Errorf("versiond runtime preflight: %w", err)
		}
	}
	if o.config.VersiondRuntime == RuntimeDocker &&
		before(journal.Phase, "host_started", replacementPhases) &&
		journal.PreviousRestartPolicy == "" {
		policy, err := o.replacementRestartPolicy()
		if err != nil {
			return err
		}
		journal.PreviousRestartPolicy = policy
		if err := o.writeJournal(journal); err != nil {
			return err
		}
	}
	if before(journal.Phase, "router_joining", replacementPhases) {
		if err := o.routerTransition(ctx, "join"); err != nil {
			return err
		}
		if err := o.advance(&journal, "router_joining"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "host_started", replacementPhases) {
		if err := o.startVersiond(ctx, journal.PreviousRestartPolicy); err != nil {
			return err
		}
		if err := o.advance(&journal, "host_started"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "host_ready", replacementPhases) {
		if err := o.waitForReady(ctx); err != nil {
			return err
		}
		if err := o.advance(&journal, "host_ready"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "router_active", replacementPhases) {
		if err := o.routerTransition(ctx, "activate"); err != nil {
			return err
		}
		if err := o.advance(&journal, "router_active"); err != nil {
			return err
		}
	}
	return o.advance(&journal, "complete")
}

// host_idle is retained so an operation journal written by the previous
// external-health polling flow can resume without repeating router mutation.
var evacuationPhases = []string{
	"started",
	"router_draining",
	"host_idle",
	"restart_disabled",
	"term_requested",
	"term_sent",
	"host_stopped",
	"router_offline",
	"complete",
}
var replacementPhases = []string{"started", "router_joining", "host_started", "host_ready", "router_active", "complete"}
var cancellationPhases = []string{"requested", "restart_restored", "router_active", "complete"}

func before(current, target string, phases []string) bool {
	currentIndex, targetIndex := -1, -1
	for i, phase := range phases {
		if phase == current {
			currentIndex = i
		}
		if phase == target {
			targetIndex = i
		}
	}
	return currentIndex < targetIndex
}

func (o *Orchestrator) routerTransition(ctx context.Context, action string) error {
	args := []string{"gonka-routerctl", "host", action, "--operation-id", o.config.OperationID}
	if o.config.ForceRouterGuard && action == "drain" {
		args = append(args, "--force")
	}
	if action == "join" && o.config.UpstreamAddress != "" {
		args = append(args, "--address", o.config.UpstreamAddress)
	}
	args = append(args, o.config.Upstream)
	if o.config.RouterRuntime == RuntimeDocker {
		args = append([]string{"docker", "exec", o.config.RouterService}, args...)
	}
	_, err := o.runRemote(ctx, o.config.RouterSSH, args...)
	return err
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
				return fmt.Errorf("replacement readiness timeout: %w", lastErr)
			}
			return errors.New("replacement readiness timeout")
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

func (o *Orchestrator) versiondRunning(ctx context.Context) (bool, error) {
	return o.versiondRuntime.Running(ctx)
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
	if commandCtx.Err() != nil {
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

func (o *Orchestrator) replacementRestartPolicy() (string, error) {
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
	if journal.SchemaVersion != 1 || journal.Mode != "evacuate" {
		return "", errors.New("restart-policy source is not an evacuation journal")
	}
	if !validJournalPhase(journal.Mode, journal.Phase) || !validCancellationState(journal) {
		return "", errors.New("restart-policy source has an invalid evacuation state")
	}
	if before(journal.Phase, "router_offline", evacuationPhases) {
		return "", fmt.Errorf(
			"evacuation journal has not reached router_offline: %s",
			journal.Phase,
		)
	}
	if !sameLogicalVersiondHost(journal.Scope, o.operationScope()) {
		return "", errors.New("evacuation journal belongs to a different logical versiond host")
	}
	if err := validateDockerRestartPolicy(journal.PreviousRestartPolicy); err != nil {
		return "", fmt.Errorf("evacuation journal restart policy: %w", err)
	}
	return journal.PreviousRestartPolicy, nil
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
	if journal.SchemaVersion != 1 {
		return Journal{}, fmt.Errorf("unsupported hostctl journal schema %d", journal.SchemaVersion)
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
	return journal, nil
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
		SchemaVersion: 1,
		OperationID:   o.config.OperationID,
		Mode:          mode,
		Scope:         o.operationScope(),
		Phase:         "started",
		UpdatedAt:     time.Now().UTC(),
	}
	return journal, o.writeJournal(journal)
}

func validJournalPhase(mode, phase string) bool {
	if mode == "evacuate" && phase == "canceled" {
		return true
	}
	phases := evacuationPhases
	if mode == "replace" {
		phases = replacementPhases
	} else if mode != "evacuate" {
		return false
	}
	for _, candidate := range phases {
		if candidate == phase {
			return true
		}
	}
	return false
}

func validCancellationState(journal Journal) bool {
	if journal.Mode != "evacuate" {
		return journal.CancellationPhase == ""
	}
	if journal.CancellationPhase == "" {
		// Journals written before cancellation checkpoints were introduced may
		// already contain the terminal canceled phase.
		return journal.Phase != "canceled" || journal.SchemaVersion == 1
	}
	found := false
	for _, phase := range cancellationPhases {
		if journal.CancellationPhase == phase {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if journal.CancellationPhase == "complete" {
		return journal.Phase == "canceled"
	}
	return before(journal.Phase, "term_requested", evacuationPhases)
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
	if phase := readOperationPhase(o.config.JournalPath); phase != "" {
		details = append(details, "journal_phase="+strconv.Quote(phase))
	}

	guidance := "wait for or interrupt the owner, then retry " + requestedAction
	if requestedAction == "cancel" {
		guidance += "; cancellation is valid only before term_requested, " +
			"otherwise resume evacuate"
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

func readOperationPhase(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var journal struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		return ""
	}
	return journal.Phase
}

func (o *Orchestrator) advance(journal *Journal, phase string) error {
	journal.Phase = phase
	journal.UpdatedAt = time.Now().UTC()
	return o.writeJournal(*journal)
}

func (o *Orchestrator) advanceCancellation(journal *Journal, phase string) error {
	journal.CancellationPhase = phase
	journal.UpdatedAt = time.Now().UTC()
	return o.writeJournal(*journal)
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
