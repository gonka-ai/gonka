package session

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"common/chain"
	"common/logging"
	"devshard/chainoracle/blocks"
	blockclient "devshard/chainoracle/blocks/client"
	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/failover"
	"devshard/heightsync"
	"devshard/host"
	"devshard/transport"

	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

const (
	envChainOracleURL  = "DEVSHARD_CHAINORACLE_URL"
	envHeightSync      = "DEVSHARD_HEIGHTSYNC"
	envHeightSyncK     = "DEVSHARD_HEIGHTSYNC_K"
	envHeightSyncSlots = "DEVSHARD_HEIGHTSYNC_SLOTS"
	envHeightSyncProbe = "DEVSHARD_HEIGHTSYNC_PROBE_INTERVAL"
)

// SetHeightSyncFromEnv wires an optional height-sync oracle. Empty
// DEVSHARD_CHAINORACLE_URL and unset DEVSHARD_HEIGHTSYNC is a no-op even when
// chainClient is non-nil (compose always has NODE_GRPC_URL). Call before
// RecoverSessions so recovered sessions pick up WithHeightSync / WithChainOracle.
func (m *HostManager) SetHeightSyncFromEnv(ctx context.Context, chainClient *chain.Client) error {
	if m == nil {
		return nil
	}
	url := strings.TrimSpace(os.Getenv(envChainOracleURL))
	if url == "" && !heightSyncFlag() {
		return nil
	}
	k, err := parseUintEnv(envHeightSyncK)
	if err != nil {
		return err
	}
	slots, err := parseUintEnv(envHeightSyncSlots)
	if err != nil {
		return err
	}
	probe, err := parseDurationEnv(envHeightSyncProbe)
	if err != nil {
		return err
	}

	var httpOracle *blockclient.Client
	if url != "" {
		cli, err := blockclient.NewHTTP(ctx, blockclient.HTTPConfig{BaseURL: url})
		if err != nil {
			return fmt.Errorf("chainoracle http client: %w", err)
		}
		httpOracle = cli
	}

	var chainOracle = direct.NewFromChain(chainClient, chainRPCFromEnv())
	if httpOracle == nil && chainClient == nil {
		return fmt.Errorf("%s set but no %s and no chain client", envHeightSync, envChainOracleURL)
	}

	var httpBlk blocks.BlockOracle
	var closer func()
	if httpOracle != nil {
		httpBlk = httpOracle
		closer = httpOracle.Close
	}
	oracle := failover.New(httpBlk, chainOracle, failover.Config{ProbeInterval: probe}, closer)

	sched, err := heightsync.NewAnchorSchedulerFromOracle(k, slots, oracle)
	if err != nil {
		if closer != nil {
			closer()
		}
		return fmt.Errorf("height-sync scheduler: %w", err)
	}

	m.chainOracle = oracle
	m.heightSync = sched
	m.heightSyncCloser = closer
	logging.Info("height sync enabled", inferenceTypes.System,
		"oracle_url", url,
		"k", sched.K(),
		"slots", sched.SlotsNum(),
		"direct_chain", chainClient != nil,
	)
	return nil
}

// CloseHeightSync stops the optional chainoracle HTTP client. Idempotent.
func (m *HostManager) CloseHeightSync() {
	if m == nil {
		return
	}
	if m.heightSyncCloser != nil {
		m.heightSyncCloser()
		m.heightSyncCloser = nil
	}
	m.chainOracle = nil
	m.heightSync = nil
}

func heightSyncFlag() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envHeightSync))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func chainRPCFromEnv() string {
	for _, name := range []string{"DEVSHARD_CHAIN_RPC", "NODE_RPC_URL", "DEVSHARD_COMET_RPC"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func parseDurationEnv(name string) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

func parseUintEnv(name string) (uint64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func (m *HostManager) transportServerOpts() []transport.ServerOption {
	opts := []transport.ServerOption{
		transport.WithBridge(m.bridge),
		transport.WithRateLimit(transport.DefaultRateLimitConfig()),
	}
	if m.heightSync != nil {
		opts = append(opts, transport.WithHeightSync(m.heightSync, m.chainOracle))
	}
	return opts
}

func (m *HostManager) appendChainOracleOpt(opts []host.HostOption) []host.HostOption {
	if m.chainOracle != nil {
		return append(opts, host.WithChainOracle(m.chainOracle))
	}
	return opts
}
