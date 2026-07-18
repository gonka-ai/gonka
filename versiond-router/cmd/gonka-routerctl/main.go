package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"versiond-router/internal/router"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gonka-routerctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	controller := router.NewController(loadConfig(), nil)
	switch args[0] {
	case "bootstrap":
		return bootstrap(ctx, controller, args[1:])
	case "host":
		return mutateHost(ctx, controller, args[1:])
	case "status":
		state, err := controller.Status(ctx)
		if err != nil {
			return err
		}
		return printJSON(state)
	case "recover":
		state, err := controller.Recover(ctx)
		if err != nil {
			return err
		}
		return printJSON(state)
	default:
		return usageError()
	}
}

func bootstrap(ctx context.Context, controller *router.Controller, args []string) error {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("bootstrap accepts no positional arguments")
	}
	hosts := strings.Fields(os.Getenv("VERSIOND_HOSTS"))
	port, err := strconv.Atoi(envOrDefault("VERSIOND_PORT", "8080"))
	if err != nil {
		return fmt.Errorf("parse VERSIOND_PORT: %w", err)
	}
	state, err := router.NewState(
		hosts,
		port,
		os.Getenv("VERSIOND_LEGACY_HOST"),
		splitList(os.Getenv("VERSIOND_NON_HA_VERSIONS")),
	)
	if err != nil {
		return err
	}
	state.LastOperation = operationID("bootstrap")
	state, err = controller.Bootstrap(ctx, state)
	if err != nil {
		return err
	}
	return printJSON(state)
}

func mutateHost(ctx context.Context, controller *router.Controller, args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	action := router.Action(args[0])
	switch action {
	case router.ActionDrain, router.ActionOffline, router.ActionJoin, router.ActionActivate:
	default:
		return fmt.Errorf("unknown host action %q", args[0])
	}
	flags := flag.NewFlagSet("host "+string(action), flag.ContinueOnError)
	force := flags.Bool("force", false, "override last-active and legacy-host guards")
	address := flags.String("address", "", "upstream address for a joining host")
	opID := flags.String("operation-id", "", "idempotent operation identifier")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("host %s requires exactly one host name", action)
	}
	if *opID == "" {
		*opID = operationID(string(action))
	}
	state, err := controller.Transition(ctx, router.Transition{
		Action:      action,
		Host:        flags.Arg(0),
		Address:     *address,
		Force:       *force,
		OperationID: *opID,
	})
	if err != nil {
		return err
	}
	return printJSON(state)
}

func loadConfig() router.Config {
	statePath := envOrDefault("VERSIOND_ROUTER_STATE", "/var/lib/gonka/versiond-router/state.json")
	return router.Config{
		StatePath:    statePath,
		AuditPath:    envOrDefault("VERSIOND_ROUTER_AUDIT", "/var/lib/gonka/versiond-router/audit.jsonl"),
		LockPath:     envOrDefault("VERSIOND_ROUTER_LOCK", "/run/gonka/versiond-router.lock"),
		JournalPath:  envOrDefault("VERSIOND_ROUTER_JOURNAL", statePath+".operation.json"),
		TemplatePath: envOrDefault("VERSIOND_ROUTER_TEMPLATE", "/etc/nginx/template/nginx.conf.template"),
		OutputPath:   envOrDefault("VERSIOND_ROUTER_OUT", "/etc/nginx/conf.d/default.conf"),
		NginxBinary:  envOrDefault("VERSIOND_ROUTER_NGINX_BIN", "nginx"),
		ProxyPolicy: router.ProxyPolicy{
			MaxBodyBytes:      parsePositiveInt64Env("VERSIOND_ROUTER_MAX_BODY_BYTES", 10*1024*1024),
			ConnectTimeout:    parsePositiveDurationEnv("VERSIOND_ROUTER_CONNECT_TIMEOUT", 2*time.Second),
			StreamIdleTimeout: parsePositiveDurationEnv("VERSIOND_ROUTER_STREAM_IDLE_TIMEOUT", 20*time.Minute),
			UpstreamKeepalive: int(parsePositiveInt64Env("VERSIOND_ROUTER_UPSTREAM_KEEPALIVE", 64)),
		},
	}
}

func parsePositiveInt64Env(key string, fallback int64) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parsePositiveDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < time.Second {
		return fallback
	}
	return value
}

func splitList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';'
	})
}

func operationID(action string) string {
	return fmt.Sprintf("%s-%d-%d", action, time.Now().UTC().UnixNano(), os.Getpid())
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func usageError() error {
	return errors.New("usage: gonka-routerctl bootstrap | status | recover | host <drain|offline|join|activate> [flags] HOST")
}
