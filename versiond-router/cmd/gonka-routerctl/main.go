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
	config, err := loadConfig()
	if err != nil {
		return err
	}
	controller := router.NewController(config, nil)
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
	state, err := controller.Bootstrap(ctx, initialStateFromEnv)
	if err != nil {
		return err
	}
	return printJSON(state)
}

func initialStateFromEnv() (router.State, error) {
	hosts := strings.Fields(os.Getenv("VERSIOND_HOSTS"))
	port, err := strconv.Atoi(envOrDefault("VERSIOND_PORT", "8080"))
	if err != nil {
		return router.State{}, fmt.Errorf("parse VERSIOND_PORT: %w", err)
	}
	state, err := router.NewState(
		hosts,
		port,
		os.Getenv("VERSIOND_LEGACY_HOST"),
		splitList(os.Getenv("VERSIOND_NON_HA_VERSIONS")),
	)
	if err != nil {
		return router.State{}, err
	}
	state.LastOperation = operationID("bootstrap")
	return state, nil
}

func mutateHost(ctx context.Context, controller *router.Controller, args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "transfer":
		return transferHost(ctx, controller, args[1:])
	case "add":
		return addHost(ctx, controller, args[1:])
	case "cancel":
		return cancelHost(ctx, controller, args[1:])
	default:
		return fmt.Errorf("unknown host command %q", args[0])
	}
}

func transferHost(
	ctx context.Context,
	controller *router.Controller,
	args []string,
) error {
	flags := flag.NewFlagSet("host transfer", flag.ContinueOnError)
	from := flags.String("from", "", "expected current membership state")
	to := flags.String("to", "", "next membership state")
	target := flags.String("target", "", "final transfer state")
	force := flags.Bool("force", false, "override drain and legacy data-migration guards")
	address := flags.String("address", "", "upstream address used by a join transfer")
	legacyHost := flags.String("legacy-host", "", "new legacy owner when removing the current legacy host")
	opID := flags.String("operation-id", "", "idempotent operation identifier")
	membershipID := flags.String("membership-id", "", "expected immutable membership identifier")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("host transfer requires exactly one host name")
	}
	if *from == "" || *to == "" || *target == "" {
		return errors.New("host transfer requires --from, --to, and --target")
	}
	if *opID == "" {
		*opID = operationID("transfer")
	}
	state, err := controller.Transition(ctx, router.Transition{
		OperationID:  *opID,
		MembershipID: *membershipID,
		Host:         flags.Arg(0),
		From:         router.HostState(*from),
		To:           router.HostState(*to),
		Target:       router.HostState(*target),
		Address:      *address,
		LegacyHost:   *legacyHost,
		Force:        *force,
	})
	if err != nil {
		return err
	}
	return printJSON(state)
}

func addHost(ctx context.Context, controller *router.Controller, args []string) error {
	flags := flag.NewFlagSet("host add", flag.ContinueOnError)
	address := flags.String("address", "", "upstream address for the new membership")
	opID := flags.String("operation-id", "", "idempotent operation identifier")
	membershipID := flags.String("membership-id", "", "explicit membership identifier")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("host add requires exactly one host name")
	}
	if *opID == "" {
		*opID = operationID("add")
	}
	state, err := controller.Add(ctx, router.AddMembership{
		OperationID:  *opID,
		MembershipID: *membershipID,
		Host:         flags.Arg(0),
		Address:      *address,
	})
	if err != nil {
		return err
	}
	return printJSON(state)
}

func cancelHost(ctx context.Context, controller *router.Controller, args []string) error {
	flags := flag.NewFlagSet("host cancel", flag.ContinueOnError)
	opID := flags.String("operation-id", "", "operation identifier that owns the transfer")
	membershipID := flags.String("membership-id", "", "expected immutable membership identifier")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("host cancel requires exactly one host name")
	}
	if *opID == "" {
		return errors.New("host cancel requires --operation-id")
	}
	state, err := controller.Cancel(ctx, router.CancelTransfer{
		OperationID:  *opID,
		MembershipID: *membershipID,
		Host:         flags.Arg(0),
	})
	if err != nil {
		return err
	}
	return printJSON(state)
}

func loadConfig() (router.Config, error) {
	statePath := envOrDefault("VERSIOND_ROUTER_STATE", "/var/lib/gonka/versiond-router/state.json")
	maxBodyBytes, err := parsePositiveInt64Env(
		"VERSIOND_ROUTER_MAX_BODY_BYTES",
		10*1024*1024,
	)
	if err != nil {
		return router.Config{}, err
	}
	connectTimeout, err := parsePositiveDurationEnv(
		"VERSIOND_ROUTER_CONNECT_TIMEOUT",
		2*time.Second,
	)
	if err != nil {
		return router.Config{}, err
	}
	streamIdleTimeout, err := parsePositiveDurationEnv(
		"VERSIOND_ROUTER_STREAM_IDLE_TIMEOUT",
		20*time.Minute,
	)
	if err != nil {
		return router.Config{}, err
	}
	upstreamKeepalive, err := parsePositiveInt64Env(
		"VERSIOND_ROUTER_UPSTREAM_KEEPALIVE",
		64,
	)
	if err != nil {
		return router.Config{}, err
	}
	if upstreamKeepalive > int64(^uint(0)>>1) {
		return router.Config{}, fmt.Errorf(
			"VERSIOND_ROUTER_UPSTREAM_KEEPALIVE %d exceeds platform int range",
			upstreamKeepalive,
		)
	}
	return router.Config{
		StatePath:    statePath,
		AuditPath:    envOrDefault("VERSIOND_ROUTER_AUDIT", "/var/lib/gonka/versiond-router/audit.jsonl"),
		LockPath:     envOrDefault("VERSIOND_ROUTER_LOCK", "/run/gonka/versiond-router.lock"),
		JournalPath:  envOrDefault("VERSIOND_ROUTER_JOURNAL", statePath+".operation.json"),
		TemplatePath: envOrDefault("VERSIOND_ROUTER_TEMPLATE", "/etc/nginx/template/nginx.conf.template"),
		OutputPath:   envOrDefault("VERSIOND_ROUTER_OUT", "/etc/nginx/conf.d/default.conf"),
		NginxBinary:  envOrDefault("VERSIOND_ROUTER_NGINX_BIN", "nginx"),
		ProxyPolicy: router.ProxyPolicy{
			MaxBodyBytes:      maxBodyBytes,
			ConnectTimeout:    connectTimeout,
			StreamIdleTimeout: streamIdleTimeout,
			UpstreamKeepalive: int(upstreamKeepalive),
		},
	}, nil
}

func parsePositiveInt64Env(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", key, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %q", key, raw)
	}
	return value, nil
}

func parsePositiveDurationEnv(
	key string,
	fallback time.Duration,
) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", key, raw, err)
	}
	if value < time.Second {
		return 0, fmt.Errorf("%s must be at least 1s, got %q", key, raw)
	}
	return value, nil
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
	return errors.New(
		"usage: gonka-routerctl bootstrap | status | recover | " +
			"host <transfer|add|cancel> [flags] HOST",
	)
}
