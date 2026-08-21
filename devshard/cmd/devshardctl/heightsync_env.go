package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"common/chain"
	"devshard/chainoracle/blocks"
	blockclient "devshard/chainoracle/blocks/client"
	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/tipcache"
	"devshard/heightsync"
	"devshard/transport"
)

const (
	envChainOracleURL  = "DEVSHARD_CHAINORACLE_URL"
	envHeightSync      = "DEVSHARD_HEIGHTSYNC"
	envHeightSyncK     = "DEVSHARD_HEIGHTSYNC_K"
	envHeightSyncSlots = "DEVSHARD_HEIGHTSYNC_SLOTS"
	envHeightSyncProbe = "DEVSHARD_HEIGHTSYNC_PROBE_INTERVAL"
	envLogLevel        = "DEVSHARD_LOG_LEVEL"
)

type heightSyncProcessState struct {
	sched  *heightsync.AnchorScheduler
	oracle blocks.BlockOracle
	closer func()
}

var (
	hsOnce sync.Once
	hsSt   *heightSyncProcessState
	hsErr  error
)

// initGatewaySlog raises slog when DEVSHARD_LOG_LEVEL is set.
// Unset leaves the Go default (Info), matching today's gateway logs.
func initGatewaySlog() {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envLogLevel)))
	if raw == "" {
		return
	}
	level := slog.LevelInfo
	switch raw {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
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

func heightSyncFlag() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envHeightSync))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func cometRPCForHeightSync(grpcURL string) string {
	if rpc := effectiveChainRPC(); rpc != "" {
		return rpc
	}
	return chain.RPCURLFromGRPCURL(grpcURL)
}

// initGatewayHeightSync wires the process-level scheduler after the chain
// client exists so failover can use direct chain. cometRPC is the Comet
// WebSocket endpoint (same NewBlock feed hosts use). Safe to call once.
func initGatewayHeightSync(chainClient *chain.Client, cometRPC string) error {
	_, err := loadHeightSyncProcessState(chainClient, cometRPC)
	return err
}

func loadHeightSyncProcessState(chainClient *chain.Client, cometRPC string) (*heightSyncProcessState, error) {
	hsOnce.Do(func() {
		url := strings.TrimSpace(os.Getenv(envChainOracleURL))
		if url == "" && !heightSyncFlag() {
			return
		}
		k, err := parseUintEnv(envHeightSyncK)
		if err != nil {
			hsErr = err
			return
		}
		slots, err := parseUintEnv(envHeightSyncSlots)
		if err != nil {
			hsErr = err
			return
		}
		if _, err := parseDurationEnv(envHeightSyncProbe); err != nil {
			hsErr = err
			return
		}

		var lookup *blockclient.Lookup
		if url != "" {
			cli, err := blockclient.NewLookup(blockclient.HTTPConfig{BaseURL: url})
			if err != nil {
				hsErr = fmt.Errorf("chainoracle lookup client: %w", err)
				return
			}
			lookup = cli
		}
		rpc := strings.TrimSpace(cometRPC)
		if rpc == "" {
			rpc = effectiveChainRPC()
		}
		var chainOracle blocks.BlockOracle
		if chainClient != nil || rpc != "" {
			chainOracle = direct.NewFromChain(chainClient, rpc)
		}
		if lookup == nil && chainOracle == nil && rpc == "" {
			hsErr = fmt.Errorf("%s set but no %s and no chain client", envHeightSync, envChainOracleURL)
			return
		}

		cache := tipcache.New(0)
		feedCtx, cancelFeed := context.WithCancel(context.Background())
		if rpc != "" {
			if err := tipcache.StartComet(feedCtx, rpc, cache); err != nil {
				slog.Warn("height-sync comet feed", "err", err, "rpc", rpc)
			}
		}
		var hist failover.History
		if lookup != nil {
			hist = lookup
		}
		closer := func() {
			cancelFeed()
			if lookup != nil {
				lookup.Close()
			}
		}
		oracle := failover.New(cache, hist, chainOracle)
		sched, err := heightsync.NewAnchorSchedulerFromOracle(k, slots, oracle)
		if err != nil {
			closer()
			hsErr = fmt.Errorf("height-sync scheduler: %w", err)
			return
		}
		hsSt = &heightSyncProcessState{sched: sched, oracle: oracle, closer: closer}
		slog.Info("height sync enabled", "oracle_url", url, "k", sched.K(), "slots", sched.SlotsNum(), "direct_chain", chainOracle != nil, "comet_rpc", rpc)
	})
	return hsSt, hsErr
}

// extraClientConfigFromEnv returns a per-session ClientConfig when height-sync
// is opted in. Peer-tip caches are not shared across escrows. Nil, nil when unset.
func extraClientConfigFromEnv() (*transport.ClientConfig, error) {
	st, err := loadHeightSyncProcessState(nil, effectiveChainRPC())
	if err != nil || st == nil {
		return nil, err
	}
	return &transport.ClientConfig{
		HeightSync:          st.sched,
		HeightSyncLogOracle: st.oracle,
		HeightSyncPeerTips:  transport.NewHeightSyncPeerTips(),
	}, nil
}
