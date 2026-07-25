package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"versiond-router/internal/hostctl"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gonka-hostctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 ||
		(args[0] != "add" &&
			args[0] != "evacuate" &&
			args[0] != "decommission" &&
			args[0] != "replace" &&
			args[0] != "cancel") {
		return errors.New(
			"usage: gonka-hostctl <add|evacuate|decommission|replace|cancel> [flags]",
		)
	}
	mode := args[0]
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	routerSSH := flags.String("router-ssh", "", "SSH destination for the router host")
	routerRuntime := flags.String("router-runtime", "docker", "router runtime: docker or systemd")
	routerService := flags.String("router-service", "versiond-router", "router container or systemd unit")
	upstream := flags.String("upstream", "", "router upstream host name")
	upstreamAddress := flags.String("upstream-address", "", "replacement address for the joining upstream")
	legacyHost := flags.String("legacy-host", "", "new legacy owner when decommissioning the current owner")
	versiondSSH := flags.String("versiond-ssh", "", "SSH destination for the versiond host")
	versiondRuntime := flags.String("versiond-runtime", "docker", "versiond runtime: docker or systemd")
	versiondService := flags.String("versiond-service", "", "versiond container or systemd unit")
	operationID := flags.String("operation-id", "", "stable operation identifier used for resume")
	journalPath := flags.String("journal", "", "local operation checkpoint path")
	evacuationJournal := flags.String("evacuation-journal", "", "completed evacuation journal used to restore Docker policy")
	readyTimeout := flags.Duration("ready-timeout", durationEnv("ROUTER_READY_TIMEOUT", 15*time.Minute), "maximum replacement readiness wait")
	pollInterval := flags.Duration("poll-interval", durationEnv("ROUTER_DRAIN_POLL_INTERVAL", 2*time.Second), "readiness/process poll interval")
	killGrace := flags.Duration("kill-grace", durationEnv("ROUTER_DRAIN_KILL_GRACE", 30*time.Minute), "wait after SIGTERM before SIGKILL")
	commandTimeout := flags.Duration("command-timeout", durationEnv("ROUTER_COMMAND_TIMEOUT", 30*time.Second), "timeout for one SSH or local command")
	readinessURL := flags.String(
		"ready-url",
		hostctl.DefaultReadinessURL,
		"versiond readiness URL on its host",
	)
	dockerRestartPolicy := flags.String("docker-restart-policy", "", "explicit restart policy applied during replacement")
	force := flags.Bool(
		"force-router-guard",
		false,
		"override drain capacity and legacy data-migration guards",
	)
	allowAbsentRuntime := flags.Bool(
		"allow-absent-runtime",
		false,
		"allow an active router membership whose runtime is already absent",
	)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *upstreamAddress != "" && mode != "add" && mode != "replace" {
		return errors.New("--upstream-address is valid only for add or replace")
	}
	if *legacyHost != "" && mode != "decommission" && mode != "cancel" {
		return errors.New("--legacy-host is valid only for decommission or its cancellation")
	}
	if *evacuationJournal != "" && mode != "replace" {
		return errors.New("--evacuation-journal is valid only for replace")
	}
	if *allowAbsentRuntime && mode != "evacuate" && mode != "decommission" {
		return errors.New(
			"--allow-absent-runtime is valid only for evacuate or decommission",
		)
	}
	if *operationID == "" {
		return errors.New("--operation-id is required so the operation can be resumed")
	}
	stateDir, err := hostctlStateDir()
	if err != nil {
		return err
	}
	if *journalPath == "" {
		*journalPath = filepath.Join(stateDir, *operationID+".json")
	}
	orchestrator, err := hostctl.New(hostctl.Config{
		RouterSSH:           *routerSSH,
		RouterRuntime:       hostctl.Runtime(*routerRuntime),
		RouterService:       *routerService,
		Upstream:            *upstream,
		UpstreamAddress:     *upstreamAddress,
		LegacyHost:          *legacyHost,
		VersiondSSH:         *versiondSSH,
		VersiondRuntime:     hostctl.Runtime(*versiondRuntime),
		VersiondService:     *versiondService,
		OperationID:         *operationID,
		JournalPath:         *journalPath,
		StateDir:            stateDir,
		EvacuationJournal:   *evacuationJournal,
		ReadyTimeout:        *readyTimeout,
		PollInterval:        *pollInterval,
		KillGrace:           *killGrace,
		CommandTimeout:      *commandTimeout,
		ReadinessURL:        *readinessURL,
		DockerRestartPolicy: *dockerRestartPolicy,
		ForceRouterGuard:    *force,
		AllowAbsentRuntime:  *allowAbsentRuntime,
	}, hostctl.SSHRemote{})
	if err != nil {
		return err
	}
	switch mode {
	case "add":
		return orchestrator.Add(ctx)
	case "evacuate":
		return orchestrator.Evacuate(ctx)
	case "decommission":
		return orchestrator.Decommission(ctx)
	case "cancel":
		return orchestrator.Cancel(ctx)
	default:
		return orchestrator.Replace(ctx)
	}
}

func hostctlStateDir() (string, error) {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		if !filepath.IsAbs(stateHome) {
			return "", errors.New("XDG_STATE_HOME must be an absolute path")
		}
		return filepath.Join(stateHome, "gonka", "hostctl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for hostctl state: %w", err)
	}
	return filepath.Join(home, ".local", "state", "gonka", "hostctl"), nil
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}
