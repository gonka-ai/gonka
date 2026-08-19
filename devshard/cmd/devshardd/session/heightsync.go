package session

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	blockclient "devshard/chainoracle/blocks/client"
	"devshard/heightsync"
	"devshard/host"
	"devshard/transport"

	"common/logging"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

const (
	envChainOracleURL  = "DEVSHARD_CHAINORACLE_URL"
	envHeightSyncK     = "DEVSHARD_HEIGHTSYNC_K"
	envHeightSyncSlots = "DEVSHARD_HEIGHTSYNC_SLOTS"
)

// SetHeightSyncFromEnv wires an optional HTTP chainoracle when
// DEVSHARD_CHAINORACLE_URL is set. Empty env is a no-op so current host
// behaviour is unchanged. Call before RecoverSessions so recovered
// sessions pick up WithHeightSync / WithChainOracle.
func (m *HostManager) SetHeightSyncFromEnv(ctx context.Context) error {
	if m == nil {
		return nil
	}
	url := strings.TrimSpace(os.Getenv(envChainOracleURL))
	if url == "" {
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

	cli, err := blockclient.NewHTTP(ctx, blockclient.HTTPConfig{BaseURL: url})
	if err != nil {
		return fmt.Errorf("chainoracle http client: %w", err)
	}
	sched, err := heightsync.NewAnchorSchedulerFromOracle(k, slots, cli)
	if err != nil {
		cli.Close()
		return fmt.Errorf("height-sync scheduler: %w", err)
	}

	m.chainOracle = cli
	m.heightSync = sched
	m.heightSyncCloser = cli.Close
	logging.Info("height sync enabled", inferenceTypes.System,
		"oracle_url", url,
		"k", sched.K(),
		"slots", sched.SlotsNum(),
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
