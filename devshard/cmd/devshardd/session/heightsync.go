package session

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"common/chain"
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmclient"
	"common/logging"
	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/headertime"
	"devshard/chainoracle/blocks/tipcache"
	"devshard/heightsync"
	"devshard/host"
	"devshard/hostevents"
	"devshard/transport"

	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

const (
	envHeightSyncK     = "DEVSHARD_HEIGHTSYNC_K"
	envHeightSyncSlots = "DEVSHARD_HEIGHTSYNC_SLOTS"
	envHeightSyncProbe = "DEVSHARD_HEIGHTSYNC_PROBE_INTERVAL"
)

// SetHeightSyncFromEnv wires the height-sync oracle when a NodeManager
// client or a chain client is available. No-op only when neither exists.
// Call before RecoverSessions so recovered sessions pick up WithHeightSync
// / WithChainOracle.
//
// Latest is a live Comet NewBlock cache (WS up and Observe younger than
// CometMaxAge), then NodeManager GetBlockHeader (height 0) unless that
// RPC is Unimplemented (retried every 15m) or in 1m/5m/15m backoff, then chain GetLatestBlock
// at most once per ChainRefresh. HTTP GET /block is not used. At() uses
// the cached window, then GetBlockHeader / GetBlockByHeight for heights
// inside AtLookback of the known tip (same nm skip as Latest). A miss
// returns a dummy header so L6 does not mark.
func (m *HostManager) SetHeightSyncFromEnv(ctx context.Context, chainClient *chain.Client, nm gen.NodeManagerClient) error {
	if m == nil {
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
	if _, err := parseDurationEnv(envHeightSyncProbe); err != nil {
		return err
	}

	var nmOracle blocks.BlockOracle
	if nm != nil {
		cli, err := nmclient.New(nm)
		if err != nil {
			return fmt.Errorf("chainoracle node-manager client: %w", err)
		}
		nmOracle = cli
	}

	var chainOracle blocks.BlockOracle
	if chainClient != nil {
		chainOracle = direct.NewFromChain(chainClient)
	}
	if nmOracle == nil && chainOracle == nil {
		return nil
	}

	cache := tipcache.New(failover.CometMaxAge)
	var oracle blocks.BlockOracle = failover.New(cache, nmOracle, chainOracle)
	if d, fab := testenvOracleFromEnv(); d != 0 || fab {
		oracle = wrapTestenvOracleOverlay(oracle, d, fab)
		logging.Info("height sync testenv oracle overlay", inferenceTypes.System,
			"height_delta", d,
			"fabricate_hash", fab,
		)
	}

	sched, err := heightsync.NewAnchorSchedulerFromOracle(k, slots, oracle)
	if err != nil {
		return fmt.Errorf("height-sync scheduler: %w", err)
	}

	m.chainOracle = oracle
	m.headerTimes = headertime.New(headertime.WithFiller(headertime.OracleFiller(oracle, headertime.OriginChain, false)))
	m.heightSync = sched
	m.heightSyncTip = cache
	m.cometLiveness = func(ok bool) {
		if s, has := oracle.(interface{ SetCometConnected(bool) }); has {
			s.SetCometConnected(ok)
		}
	}
	logging.Info("height sync enabled", inferenceTypes.System,
		"node_manager", nmOracle != nil,
		"k", sched.K(),
		"slots", sched.SlotsNum(),
		"direct_chain", chainOracle != nil,
	)
	return nil
}

// HeaderTimes is the process header-time map.
func (m *HostManager) HeaderTimes() *headertime.Source {
	if m == nil {
		return nil
	}
	return m.headerTimes
}

// SetHostEventPoller keeps the process poller so status can read BlockFeedLive.
func (m *HostManager) SetHostEventPoller(p *hostevents.Poller) {
	if m == nil {
		return
	}
	m.hostEvents = p
}

// HostEventPoller is the process GetHostEvents loop. Nil when it has not started.
func (m *HostManager) HostEventPoller() *hostevents.Poller {
	if m == nil {
		return nil
	}
	return m.hostEvents
}

// AttachHeaderTimeCache restores an already-caught-up map from local
// SQLite or Postgres. A failed open leaves the in-memory map empty.
func (m *HostManager) AttachHeaderTimeCache(ctx context.Context, sqlitePath string) {
	if m == nil || m.headerTimes == nil || sqlitePath == "" {
		return
	}
	c, err := headertime.OpenCache(ctx, sqlitePath)
	if err != nil {
		logging.Warn("header times: cache unavailable", inferenceTypes.System, "error", err, "path", sqlitePath)
		return
	}
	m.headerTimes.SetCache(c)
}

// SeedHeaderTimes stores the oracle's latest header before the Comet
// subscription starts. Later NewBlock events extend that tip. A failed
// read leaves the map empty; the subscription still starts.
func (m *HostManager) SeedHeaderTimes(ctx context.Context) {
	if m == nil || m.headerTimes == nil || m.chainOracle == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h, err := m.chainOracle.Latest(ctx)
	if err != nil || h == nil || h.Height <= 0 || blocks.IsDummyHeader(h) {
		logging.Warn("header times: latest seed", inferenceTypes.System, "error", err)
		return
	}
	m.headerTimes.Observe(h, headertime.OriginChain, false)
	logging.Info("header times: seeded latest", inferenceTypes.System, "height", h.Height)
}

// ObserveChainHeader records a Comet NewBlock on the height-sync tip cache.
func (m *HostManager) ObserveChainHeader(h *blocks.Header) {
	if m == nil {
		return
	}
	tip := m.heightSyncTip
	if tip == nil {
		logging.Debug("heightsync: comet observe skipped", inferenceTypes.System, "reason", "no cache")
		return
	}
	if h == nil || h.Height <= 0 || blocks.IsDummyHeader(h) {
		height := int64(0)
		hashLen := 0
		if h != nil {
			height = h.Height
			hashLen = len(h.BlockHash)
		}
		logging.Debug("heightsync: comet observe skipped", inferenceTypes.System,
			"reason", "unusable", "height", height, "hash_len", hashLen)
		return
	}
	logging.Debug("heightsync: comet observe", inferenceTypes.System,
		"height", h.Height, "hash_len", len(h.BlockHash), "chain_id", h.ChainID)
	tip.Observe(h)
	// The host's own Comet socket writes the one header-time map. verified
	// because this process is subscribed to its own chain. The poller writes
	// the same map later with OriginDAPI.
	if m.headerTimes != nil {
		m.headerTimes.Observe(h, headertime.OriginComet, true)
	}
	m.SetCometConnected(true)
}

// SetCometConnected marks the Comet NewBlock subscription up or down so
// Latest() does not serve a frozen tip after the WS drops.
func (m *HostManager) SetCometConnected(ok bool) {
	if m == nil {
		return
	}
	fn := m.cometLiveness
	if fn != nil {
		fn(ok)
	}
}

// CloseHeightSync stops the height-sync scheduler. Idempotent. The tip
// cache, failover oracle, and Comet liveness callback stay so in-flight
// ObserveChainHeader / SetCometConnected from chain events cannot race a
// nil pointer. The NodeManager client is owned by the process ML client,
// not here.
func (m *HostManager) CloseHeightSync() {
	if m == nil {
		return
	}
	if m.heightSyncCloser != nil {
		m.heightSyncCloser()
		m.heightSyncCloser = nil
	}
	if m.headerTimes != nil {
		_ = m.headerTimes.CloseCache()
	}
	m.heightSync = nil
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
		transport.WithMaxBodySize(m.maxBodySize),
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
