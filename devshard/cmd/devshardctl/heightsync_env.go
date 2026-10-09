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
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmclient"
	"common/nodemanager"
	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/tipcache"
	"devshard/heightsync"
	"devshard/transport"
)

const (
	envHeightSyncK        = "DEVSHARD_HEIGHTSYNC_K"
	envHeightSyncSlots    = "DEVSHARD_HEIGHTSYNC_SLOTS"
	envHeightSyncProbe    = "DEVSHARD_HEIGHTSYNC_PROBE_INTERVAL"
	envLogLevel           = "DEVSHARD_LOG_LEVEL"
	envRequireHeightSeed  = "DEVSHARD_REQUIRE_HEIGHT_SEED"
	envGatewayChainOracle = "DEVSHARD_GATEWAY_CHAIN_ORACLE"
)

type heightSyncProcessState struct {
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

func cometRPCForHeightSync(grpcURL string) string {
	if rpc := effectiveChainRPC(); rpc != "" {
		return rpc
	}
	return chain.RPCURLFromGRPCURL(grpcURL)
}

// initGatewayHeightSync starts the optional chain follower after the chain
// client exists. No-op unless DEVSHARD_GATEWAY_CHAIN_ORACLE is on. The
// protocol scheduler is always PeerTipOracleSource (extraClientConfigFromEnv).
func initGatewayHeightSync(chainClient *chain.Client, cometRPC string) error {
	if !gatewayChainOracleFromEnv() {
		return nil
	}
	_, err := loadHeightSyncProcessState(chainClient, cometRPC)
	return err
}

func loadHeightSyncProcessState(chainClient *chain.Client, cometRPC string) (*heightSyncProcessState, error) {
	hsOnce.Do(func() {
		if !gatewayChainOracleFromEnv() {
			return
		}
		if _, err := parseUintEnv(envHeightSyncK); err != nil {
			hsErr = err
			return
		}
		if _, err := parseUintEnv(envHeightSyncSlots); err != nil {
			hsErr = err
			return
		}
		if _, err := parseDurationEnv(envHeightSyncProbe); err != nil {
			hsErr = err
			return
		}

		nmOracle, nmClose, err := nodeManagerOracleFromEnv()
		if err != nil {
			hsErr = err
			return
		}
		rpc := strings.TrimSpace(cometRPC)
		if rpc == "" {
			rpc = effectiveChainRPC()
		}
		var chainOracle blocks.BlockOracle
		if chainClient != nil {
			chainOracle = direct.NewFromChain(chainClient)
		}
		if nmOracle == nil && chainOracle == nil && rpc == "" {
			if nmClose != nil {
				nmClose()
			}
			return
		}

		cache := tipcache.New(failover.CometMaxAge)
		fo := failover.New(cache, nmOracle, chainOracle)
		feedCtx, cancelFeed := context.WithCancel(context.Background())
		if rpc != "" {
			if err := tipcache.StartCometWithHooks(feedCtx, rpc, cache, tipcache.CometHooks{
				OnConnected:    func() { fo.SetCometConnected(true) },
				OnDisconnected: func() { fo.SetCometConnected(false) },
			}); err != nil {
				slog.Warn("height-sync comet feed", "err", err, "rpc", rpc)
			}
		}
		closer := func() {
			cancelFeed()
			if nmClose != nil {
				nmClose()
			}
		}
		hsSt = &heightSyncProcessState{oracle: fo, closer: closer}
		slog.Info("height sync chain follower enabled",
			"node_manager", nmOracle != nil, "direct_chain", chainOracle != nil, "comet_rpc", rpc)
	})
	return hsSt, hsErr
}

func nodeManagerOracleFromEnv() (blocks.BlockOracle, func(), error) {
	addr := strings.TrimSpace(os.Getenv("NODE_MANAGER_ADDR"))
	if addr == "" {
		return nil, nil, nil
	}
	cli, err := nodemanager.NewClient(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("chainoracle node-manager dial: %w", err)
	}
	lookup, err := nmclient.New(cli.NodeManagerClient())
	if err != nil {
		_ = cli.Close()
		return nil, nil, err
	}
	return lookup, func() { _ = cli.Close() }, nil
}

func heightSyncCourierSourcesPresent() bool {
	if strings.TrimSpace(os.Getenv("NODE_MANAGER_ADDR")) != "" {
		return true
	}
	if effectiveChainRPC() != "" {
		return true
	}
	// Testermint's gateway only injects DEVSHARD_CHAIN_GRPC; courier seed
	// POSTs to hosts and does not need Comet RPC or NodeManager on the
	// gateway process itself.
	return strings.TrimSpace(firstNonEmpty(os.Getenv("DEVSHARD_CHAIN_GRPC"), os.Getenv("NODE_GRPC_URL"))) != ""
}

// extraClientConfigFromEnv returns a per-session courier ClientConfig when
// NODE_MANAGER_ADDR, chain RPC, or chain gRPC is available. The scheduler is
// always PeerTipOracleSource over a fresh HeightSyncPeerTips. The optional
// chain follower is HeightSyncLogOracle only (DEVSHARD_GATEWAY_CHAIN_ORACLE).
// Nil, nil when neither source exists.
func extraClientConfigFromEnv() (*transport.ClientConfig, error) {
	k, err := parseUintEnv(envHeightSyncK)
	if err != nil {
		return nil, err
	}
	slots, err := parseUintEnv(envHeightSyncSlots)
	if err != nil {
		return nil, err
	}
	if _, err := parseDurationEnv(envHeightSyncProbe); err != nil {
		return nil, err
	}
	if !heightSyncCourierSourcesPresent() {
		return nil, nil
	}
	peerTips := transport.NewHeightSyncPeerTips()
	sched, err := heightsync.NewAnchorScheduler(k, slots, heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness))
	if err != nil {
		return nil, err
	}
	cfg := &transport.ClientConfig{
		HeightSync:         sched,
		HeightSyncPeerTips: peerTips,
	}
	if gatewayChainOracleFromEnv() {
		st, err := loadHeightSyncProcessState(nil, effectiveChainRPC())
		if err != nil {
			return nil, err
		}
		if st != nil {
			cfg.HeightSyncLogOracle = st.oracle
		}
	}
	return cfg, nil
}

// requireHeightSeedFromEnv is the gateway fail-closed seed gate. Unset and
// unrecognized values keep the gate on. Only 0/false/off/no disable it.
func requireHeightSeedFromEnv() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envRequireHeightSeed)))
	switch raw {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// gatewayChainOracleFromEnv is the optional verification follower. Only
// true/1/on (case-insensitive) enable it. Unset, empty, false/0/off, and
// any other value leave the follower unbuilt.
func gatewayChainOracleFromEnv() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envGatewayChainOracle)))
	switch raw {
	case "true", "1", "on":
		return true
	default:
		return false
	}
}
