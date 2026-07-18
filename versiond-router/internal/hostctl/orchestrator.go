package hostctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
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
	DrainTimeout        time.Duration
	PollInterval        time.Duration
	KillGrace           time.Duration
	DockerRestartPolicy string
	ForceRouterGuard    bool
}

type HealthSummary struct {
	SchemaVersion int    `json:"schema_version"`
	State         string `json:"state"`
	Ready         bool   `json:"ready"`
	Accepting     bool   `json:"accepting"`
	Inflight      int64  `json:"inflight"`
	InflightKnown bool   `json:"inflight_known"`
	Idle          bool   `json:"idle"`
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
	DockerRestartPolicy string  `json:"docker_restart_policy,omitempty"`
}

type Journal struct {
	SchemaVersion         int            `json:"schema_version"`
	OperationID           string         `json:"operation_id"`
	Mode                  string         `json:"mode"`
	Scope                 OperationScope `json:"scope"`
	Phase                 string         `json:"phase"`
	DrainTimedOut         bool           `json:"drain_timed_out,omitempty"`
	PreviousRestartPolicy string         `json:"previous_restart_policy,omitempty"`
	LastHealth            *HealthSummary `json:"last_health,omitempty"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

type Orchestrator struct {
	config Config
	remote Remote
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
	if config.DrainTimeout <= 0 || config.PollInterval <= 0 || config.KillGrace <= 0 {
		return nil, errors.New("drain timeout, poll interval, and kill grace must be positive")
	}
	if config.DockerRestartPolicy == "" {
		config.DockerRestartPolicy = "unless-stopped"
	}
	if config.JournalPath == "" {
		return nil, errors.New("journal path is required")
	}
	if remote == nil {
		remote = SSHRemote{}
	}
	return &Orchestrator{config: config, remote: remote}, nil
}

func validRuntime(runtime Runtime) bool {
	return runtime == RuntimeDocker || runtime == RuntimeSystemd
}

func (o *Orchestrator) Evacuate(ctx context.Context) error {
	return o.withOperationLock(ctx, func() error {
		return o.evacuate(ctx)
	})
}

func (o *Orchestrator) evacuate(ctx context.Context) error {
	journal, err := o.loadOrCreateJournal("evacuate")
	if err != nil {
		return err
	}
	if before(journal.Phase, "router_draining", evacuationPhases) {
		if err := o.routerTransition(ctx, "drain"); err != nil {
			return err
		}
		if err := o.advance(&journal, "router_draining"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "host_idle", evacuationPhases) {
		health, timedOut, err := o.waitForIdle(ctx)
		journal.LastHealth = health
		journal.DrainTimedOut = timedOut
		if err != nil {
			return err
		}
		if timedOut {
			slog.Warn(
				"router drain timeout expired; proceeding with versiond shutdown",
				"upstream", o.config.Upstream,
				"inflight", health.Inflight,
				"inflight_known", health.InflightKnown,
			)
		}
		if err := o.advance(&journal, "host_idle"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "term_sent", evacuationPhases) {
		if o.config.VersiondRuntime == RuntimeDocker && before(journal.Phase, "restart_disabled", evacuationPhases) {
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
			if err := o.advance(&journal, "restart_disabled"); err != nil {
				return err
			}
		}
		running, err := o.versiondRunning(ctx)
		if err != nil {
			return err
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

func (o *Orchestrator) Replace(ctx context.Context) error {
	return o.withOperationLock(ctx, func() error {
		return o.replace(ctx)
	})
}

func (o *Orchestrator) replace(ctx context.Context) error {
	journal, err := o.loadOrCreateJournal("replace")
	if err != nil {
		return err
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
		if err := o.startVersiond(ctx); err != nil {
			return err
		}
		if err := o.advance(&journal, "host_started"); err != nil {
			return err
		}
	}
	if before(journal.Phase, "host_ready", replacementPhases) {
		health, err := o.waitForReady(ctx)
		journal.LastHealth = health
		if err != nil {
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

var evacuationPhases = []string{"started", "router_draining", "host_idle", "restart_disabled", "term_sent", "host_stopped", "router_offline", "complete"}
var replacementPhases = []string{"started", "router_joining", "host_started", "host_ready", "router_active", "complete"}

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
	_, err := o.remote.Run(ctx, o.config.RouterSSH, args...)
	return err
}

func (o *Orchestrator) health(ctx context.Context) (*HealthSummary, error) {
	url := "http://127.0.0.1:8080/healthz?summary=1"
	var args []string
	if o.config.VersiondRuntime == RuntimeDocker {
		args = []string{"docker", "exec", o.config.VersiondService, "wget", "-q", "-O", "-", url}
	} else {
		args = []string{"curl", "--fail", "--silent", "--show-error", url}
	}
	output, err := o.remote.Run(ctx, o.config.VersiondSSH, args...)
	if err != nil {
		return nil, err
	}
	var health HealthSummary
	if err := json.Unmarshal([]byte(output), &health); err != nil {
		return nil, fmt.Errorf("decode versiond health: %w", err)
	}
	if health.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported versiond health schema %d", health.SchemaVersion)
	}
	return &health, nil
}

func (o *Orchestrator) waitForIdle(ctx context.Context) (*HealthSummary, bool, error) {
	deadline := time.Now().Add(o.config.DrainTimeout)
	var last *HealthSummary
	for {
		health, err := o.health(ctx)
		if err == nil {
			last = health
			if health.Idle && health.InflightKnown && health.Inflight == 0 {
				return health, false, nil
			}
		}
		if time.Now().After(deadline) {
			if last == nil {
				if err != nil {
					return nil, false, fmt.Errorf("versiond health unavailable until drain timeout: %w", err)
				}
				return nil, false, errors.New("versiond health unavailable until drain timeout")
			}
			return last, true, nil
		}
		if err := wait(ctx, o.config.PollInterval); err != nil {
			return last, false, err
		}
	}
}

func (o *Orchestrator) waitForReady(ctx context.Context) (*HealthSummary, error) {
	deadline := time.Now().Add(o.config.DrainTimeout)
	var lastErr error
	for {
		health, err := o.health(ctx)
		if err == nil && health.Ready && health.Accepting && health.State == "serving" {
			return health, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("replacement readiness timeout: %w", lastErr)
			}
			return health, errors.New("replacement readiness timeout")
		}
		if err := wait(ctx, o.config.PollInterval); err != nil {
			return nil, err
		}
	}
}

func (o *Orchestrator) signalVersiond(ctx context.Context, signal string) error {
	var args []string
	if o.config.VersiondRuntime == RuntimeDocker {
		args = []string{"docker", "kill", "--signal", signal, o.config.VersiondService}
	} else if signal == "TERM" {
		args = []string{"systemctl", "stop", "--no-block", o.config.VersiondService}
	} else {
		args = []string{"systemctl", "kill", "--kill-who=all", "--signal", signal, o.config.VersiondService}
	}
	_, err := o.remote.Run(ctx, o.config.VersiondSSH, args...)
	return err
}

func (o *Orchestrator) startVersiond(ctx context.Context) error {
	var args []string
	if o.config.VersiondRuntime == RuntimeDocker {
		if err := o.setDockerRestartPolicy(ctx, o.config.DockerRestartPolicy); err != nil {
			return err
		}
		args = []string{"docker", "start", o.config.VersiondService}
	} else {
		args = []string{"systemctl", "start", o.config.VersiondService}
	}
	_, err := o.remote.Run(ctx, o.config.VersiondSSH, args...)
	return err
}

func (o *Orchestrator) dockerRestartPolicy(ctx context.Context) (string, error) {
	output, err := o.remote.Run(
		ctx,
		o.config.VersiondSSH,
		"docker", "inspect", "--format", "{{.HostConfig.RestartPolicy.Name}}", o.config.VersiondService,
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (o *Orchestrator) setDockerRestartPolicy(ctx context.Context, policy string) error {
	_, err := o.remote.Run(
		ctx,
		o.config.VersiondSSH,
		"docker", "update", "--restart="+policy, o.config.VersiondService,
	)
	return err
}

func (o *Orchestrator) versiondRunning(ctx context.Context) (bool, error) {
	var args []string
	if o.config.VersiondRuntime == RuntimeDocker {
		args = []string{"docker", "inspect", "--format", "{{.State.Running}}", o.config.VersiondService}
	} else {
		args = []string{"systemctl", "is-active", o.config.VersiondService}
	}
	output, err := o.remote.Run(ctx, o.config.VersiondSSH, args...)
	value := strings.TrimSpace(output)
	if o.config.VersiondRuntime == RuntimeDocker {
		if err != nil {
			return false, err
		}
		return value == "true", nil
	}
	if value == "inactive" || value == "failed" || value == "unknown" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value == "active" || value == "activating" || value == "deactivating", nil
}

func (o *Orchestrator) waitForStopped(ctx context.Context, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		running, err := o.versiondRunning(ctx)
		if err != nil {
			return false, err
		}
		if !running {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := wait(ctx, o.config.PollInterval); err != nil {
			return false, err
		}
	}
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

func (o *Orchestrator) loadOrCreateJournal(mode string) (Journal, error) {
	data, err := os.ReadFile(o.config.JournalPath)
	if err == nil {
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
		return journal, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Journal{}, err
	}
	journal := Journal{
		SchemaVersion: 1,
		OperationID:   o.config.OperationID,
		Mode:          mode,
		Scope:         o.operationScope(),
		Phase:         "started",
		UpdatedAt:     time.Now().UTC(),
	}
	return journal, o.writeJournal(journal)
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
		DockerRestartPolicy: o.config.DockerRestartPolicy,
	}
}

func (o *Orchestrator) withOperationLock(ctx context.Context, fn func() error) error {
	lockPath := o.config.JournalPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if err := wait(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func (o *Orchestrator) advance(journal *Journal, phase string) error {
	journal.Phase = phase
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
