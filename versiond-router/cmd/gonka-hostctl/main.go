package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"versiond-router/internal/hostctl"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gonka-hostctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 ||
		(args[0] != "evacuate" && args[0] != "replace" && args[0] != "cancel") {
		return errors.New("usage: gonka-hostctl <evacuate|replace|cancel> [flags]")
	}
	mode := args[0]
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	routerSSH := flags.String("router-ssh", "", "SSH destination for the router host")
	routerRuntime := flags.String("router-runtime", "docker", "router runtime: docker or systemd")
	routerService := flags.String("router-service", "versiond-router", "router container or systemd unit")
	upstream := flags.String("upstream", "", "router upstream host name")
	upstreamAddress := flags.String("upstream-address", "", "replacement address for the joining upstream")
	versiondSSH := flags.String("versiond-ssh", "", "SSH destination for the versiond host")
	versiondRuntime := flags.String("versiond-runtime", "docker", "versiond runtime: docker or systemd")
	versiondService := flags.String("versiond-service", "", "versiond container or systemd unit")
	operationID := flags.String("operation-id", "", "stable operation identifier used for resume")
	journalPath := flags.String("journal", "", "local operation checkpoint path")
	evacuationJournal := flags.String("evacuation-journal", "", "completed evacuation journal used to restore Docker policy")
	drainTimeout := flags.Duration("drain-timeout", durationEnv("ROUTER_DRAIN_TIMEOUT", 15*time.Minute), "maximum idle/readiness wait")
	pollInterval := flags.Duration("poll-interval", durationEnv("ROUTER_DRAIN_POLL_INTERVAL", 2*time.Second), "health/process poll interval")
	killGrace := flags.Duration("kill-grace", durationEnv("ROUTER_DRAIN_KILL_GRACE", 30*time.Minute), "wait after SIGTERM before SIGKILL")
	commandTimeout := flags.Duration("command-timeout", durationEnv("ROUTER_COMMAND_TIMEOUT", 30*time.Second), "timeout for one SSH or local command")
	healthURL := flags.String("health-url", "http://127.0.0.1:8080/healthz?summary=1", "versiond health URL on its host")
	dockerRestartPolicy := flags.String("docker-restart-policy", "", "explicit restart policy applied during replacement")
	force := flags.Bool("force-router-guard", false, "override last-host or legacy-host router guard")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *operationID == "" {
		return errors.New("--operation-id is required so the operation can be resumed")
	}
	if *journalPath == "" {
		stateDir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*journalPath = filepath.Join(stateDir, "gonka", "hostctl", *operationID+".json")
	}
	orchestrator, err := hostctl.New(hostctl.Config{
		RouterSSH:           *routerSSH,
		RouterRuntime:       hostctl.Runtime(*routerRuntime),
		RouterService:       *routerService,
		Upstream:            *upstream,
		UpstreamAddress:     *upstreamAddress,
		VersiondSSH:         *versiondSSH,
		VersiondRuntime:     hostctl.Runtime(*versiondRuntime),
		VersiondService:     *versiondService,
		OperationID:         *operationID,
		JournalPath:         *journalPath,
		EvacuationJournal:   *evacuationJournal,
		DrainTimeout:        *drainTimeout,
		PollInterval:        *pollInterval,
		KillGrace:           *killGrace,
		CommandTimeout:      *commandTimeout,
		HealthURL:           *healthURL,
		DockerRestartPolicy: *dockerRestartPolicy,
		ForceRouterGuard:    *force,
	}, hostctl.SSHRemote{})
	if err != nil {
		return err
	}
	switch mode {
	case "evacuate":
		return orchestrator.Evacuate(ctx)
	case "cancel":
		return orchestrator.CancelEvacuation(ctx)
	default:
		return orchestrator.Replace(ctx)
	}
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
