package runtimeparams

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"devshard/runtimeconfig"
)

const (
	SourceAuto     = "auto"
	SourceGRPC     = "grpc"
	SourceChain    = "chain"
	SourceAdaptive = "adaptive"
)

// EnvSettings holds runtime-config tuning from environment variables.
// Both devshardd and devshardctl honor DEVSHARDD_* names; devshardctl also
// accepts DEVSHARD_* aliases for the same knobs.
type EnvSettings struct {
	Source          string
	ServerMaxWait   time.Duration
	DeadlineSlack   time.Duration
	ChainRefresh    time.Duration
	ChainInitial    time.Duration
	GRPCStale       time.Duration
	GRPCReprobe     time.Duration
	ProbeTimeout    time.Duration
	StaleCheck      time.Duration
	FailbackProbes  int
	NodeManagerAddr string
}

func SettingsFromEnv() EnvSettings {
	s := EnvSettings{
		Source:         sourceFromEnv(),
		ServerMaxWait:  60 * time.Second,
		DeadlineSlack:  5 * time.Second,
		ChainRefresh:   60 * time.Second,
		ChainInitial:   5 * time.Second,
		GRPCStale:      runtimeconfig.DefaultGRPCStaleSeconds(),
		GRPCReprobe:    runtimeconfig.DefaultGRPCReprobeSeconds(),
		ProbeTimeout:   runtimeconfig.DefaultAdaptiveProbeTimeout(),
		StaleCheck:     runtimeconfig.DefaultStaleCheckInterval(),
		FailbackProbes: runtimeconfig.DefaultFailbackProbes(),
		NodeManagerAddr: strings.TrimSpace(firstNonEmpty(
			os.Getenv("DEVSHARD_NODE_MANAGER_ADDR"),
			os.Getenv("NODE_MANAGER_ADDR"),
			"localhost:9400",
		)),
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_RUNTIME_CONFIG_MAX_WAIT_SECONDS"), os.Getenv("DEVSHARD_RUNTIME_CONFIG_MAX_WAIT_SECONDS"))); n > 0 {
		s.ServerMaxWait = time.Duration(n) * time.Second
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS"), os.Getenv("DEVSHARD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS"))); n > 0 {
		s.DeadlineSlack = time.Duration(n) * time.Second
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS"), os.Getenv("DEVSHARD_PARAMS_CHAIN_REFRESH_SECONDS"))); n > 0 {
		s.ChainRefresh = time.Duration(n) * time.Second
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS"), os.Getenv("DEVSHARD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS"))); n > 0 {
		s.ChainInitial = time.Duration(n) * time.Second
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_PARAMS_GRPC_STALE_SECONDS"), os.Getenv("DEVSHARD_PARAMS_GRPC_STALE_SECONDS"))); n > 0 {
		s.GRPCStale = time.Duration(n) * time.Second
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_PARAMS_GRPC_REPROBE_SECONDS"), os.Getenv("DEVSHARD_PARAMS_GRPC_REPROBE_SECONDS"))); n > 0 {
		s.GRPCReprobe = time.Duration(n) * time.Second
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_PARAMS_GRPC_FAILBACK_PROBES"), os.Getenv("DEVSHARD_PARAMS_GRPC_FAILBACK_PROBES"))); n > 0 {
		s.FailbackProbes = n
	}
	if n := envInt(firstNonEmpty(os.Getenv("DEVSHARDD_PARAMS_GRPC_PROBE_TIMEOUT_SECONDS"), os.Getenv("DEVSHARD_PARAMS_GRPC_PROBE_TIMEOUT_SECONDS"))); n > 0 {
		s.ProbeTimeout = time.Duration(n) * time.Second
	}
	return s
}

func sourceFromEnv() string {
	v := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		os.Getenv("DEVSHARDD_PARAMS_SOURCE"),
		os.Getenv("DEVSHARD_PARAMS_SOURCE"),
	)))
	switch v {
	case "":
		return SourceAuto
	case SourceAuto, SourceGRPC, SourceChain:
		return v
	default:
		slog.Warn("invalid runtime params source; using auto", "got", v)
		return SourceAuto
	}
}

func envInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
