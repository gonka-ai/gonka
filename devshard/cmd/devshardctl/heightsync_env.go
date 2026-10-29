package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"common/chain"
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmclient"
	"common/chainoracle/blocks/observer"
	"common/nodemanager"
	"common/nodemanager/gen"
	"devshard/bridge"
	"devshard/chainoracle/blocks/direct"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/headertime"
	"devshard/chainoracle/blocks/tipcache"
	"devshard/heightsync"
	"devshard/hostevents"
	"devshard/state"
	"devshard/transport"
	"devshard/user"
)

const (
	envHeightSyncK         = "DEVSHARD_HEIGHTSYNC_K"
	envHeightSyncSlots     = "DEVSHARD_HEIGHTSYNC_SLOTS"
	envHeightSyncProbe     = "DEVSHARD_HEIGHTSYNC_PROBE_INTERVAL"
	envHeightSyncHeartbeat = "DEVSHARD_HEIGHTSYNC_HEARTBEAT"
	envRequireHeightSeed   = "DEVSHARD_REQUIRE_HEIGHT_SEED"
	envGatewayChainOracle  = "DEVSHARD_GATEWAY_CHAIN_ORACLE"
	envQuietSettleDelay    = "DEVSHARD_QUIET_SETTLE_DELAY"
)

type heightSyncProcessState struct {
	oracle      blocks.BlockOracle
	headerTimes *headertime.Source
	verifier    *observer.Oracle
	poller      *hostevents.Poller
	cometHeader func(*blocks.Header)
	closer      func()
}

var (
	hsOnce sync.Once
	hsSt   *heightSyncProcessState
	hsErr  error
)

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

// initGatewayHeightSync starts the chain follower after the chain client
// exists. Timeouts and the seal clock read block times through it. The
// protocol scheduler is always PeerTipOracleSource (extraClientConfigFromEnv).
func initGatewayHeightSync(chainClient *chain.Client, cometRPC string) error {
	return initGatewayHeightSyncDir(chainClient, cometRPC, "")
}

func initGatewayHeightSyncDir(chainClient *chain.Client, cometRPC, storageDir string) error {
	_, err := loadHeightSyncProcessState(chainClient, cometRPC, storageDir)
	return err
}

// gatewayHeaderTimeSource builds the gateway's one header-time map. The map
// is optional: it may stay empty when NODE_MANAGER_ADDR is unset and Comet
// seed fails. With a Comet RPC it also returns the verifying oracle. Every
// header that oracle verifies is written into the map, and the map's floor
// bounds the oracle's pending dapi buffer when Comet seed succeeded.
func gatewayHeaderTimeSource(cometRPC string, fallback blocks.BlockOracle) (*headertime.Source, *observer.Oracle) {
	opts := []headertime.Option{headertime.WithGateway()}
	if cometRPC != "" {
		verifier, err := observer.NewTendermint(observer.TendermintConfig{RPCURL: cometRPC, VerifyCommits: true})
		if err == nil {
			slog.Info("header times verified by commit signatures", "comet_rpc", cometRPC)
			opts = append(opts, headertime.WithFiller(headertime.OracleFiller(verifier, headertime.OriginChain, true)))
			src := headertime.New(opts...)
			verifier.SetPublish(publishVerified(src))
			verifier.SetPendingFloor(func() int64 { return int64(src.Floor()) })
			return src, verifier
		}
		slog.Warn("header times: verifying oracle unavailable", "err", err, "comet_rpc", cometRPC)
	}
	slog.Warn("header times are not signature-verified: no Comet RPC for the gateway")
	if fallback != nil {
		opts = append(opts, headertime.WithFiller(headertime.OracleFiller(fallback, headertime.OriginChain, false)))
	}
	return headertime.New(opts...), nil
}

func attachGatewayHeaderTimeCache(src *headertime.Source, storageDir string) {
	if src == nil || strings.TrimSpace(storageDir) == "" {
		return
	}
	path := filepath.Join(storageDir, headertime.SQLiteFileName)
	c, err := headertime.OpenCache(context.Background(), path)
	if err != nil {
		slog.Warn("header times: cache unavailable", "err", err, "path", path)
		return
	}
	src.SetCache(c)
}

// publishVerified writes a header the oracle verified into the map. A dapi
// header keeps the dapi origin. A live Comet header is comet, not a filler
// chain read.
func publishVerified(src *headertime.Source) func(*blocks.Header, bool) {
	return func(h *blocks.Header, fromDAPI bool) {
		origin := headertime.OriginComet
		if fromDAPI {
			origin = headertime.OriginDAPI
		}
		src.Observe(h, origin, true)
	}
}

// gatewayBlockVerifier is the poller's check for dapi BLOCK pages when Comet
// seed succeeded. Without a verifier the caller trusts dapi directly.
func gatewayBlockVerifier(verifier *observer.Oracle) hostevents.BlockVerifier {
	if verifier == nil {
		return nil
	}
	return verifier
}

// seedGatewayMap stores one trusted tip before the poller starts. Comet
// commit verification wins when it works. If it does not, and dapi is
// configured, GetBlockHeader(0) is trusted. An unset NODE_MANAGER_ADDR
// never fills the map. The map is optional: a failed seed leaves it empty
// and the poller starts at dapi head instead of replaying the ring.
func seedGatewayMap(ctx context.Context, verifier *observer.Oracle, dapi blocks.BlockOracle, src *headertime.Source) (cometTrusted bool) {
	if seedGatewayVerified(ctx, verifier) {
		return true
	}
	seedGatewayTrustedDAPI(ctx, dapi, src)
	return false
}

// seedGatewayVerified stores the Comet-verified trusted header. A failed
// read leaves the map empty.
func seedGatewayVerified(ctx context.Context, verifier *observer.Oracle) bool {
	if verifier == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h, err := verifier.Trusted(ctx)
	if err != nil || h == nil {
		slog.Warn("header times: trusted comet seed", "err", err)
		return false
	}
	slog.Info("header times: seeded trusted comet header", "height", h.Height)
	return true
}

// seedGatewayTrustedDAPI stores dapi's latest header as trusted. The
// gateway always trusts the dapi named by NODE_MANAGER_ADDR.
func seedGatewayTrustedDAPI(ctx context.Context, oracle blocks.BlockOracle, src *headertime.Source) bool {
	if oracle == nil || src == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h, err := oracle.Latest(ctx)
	if err != nil || h == nil || h.Height <= 0 || blocks.IsDummyHeader(h) {
		slog.Warn("header times: trusted dapi seed", "err", err)
		return false
	}
	src.Observe(h, headertime.OriginDAPI, true)
	slog.Info("header times: seeded trusted dapi header", "height", h.Height)
	return true
}

// observeGatewayCometHeader hands a Comet NewBlock to the verifier, which
// writes it into the map once its commit checks out. Without a verifier the
// websocket header is written unverified.
func observeGatewayCometHeader(h *blocks.Header) {
	if hsSt == nil {
		return
	}
	if hsSt.verifier != nil {
		hsSt.verifier.Observe(h)
		return
	}
	if hsSt.headerTimes != nil {
		hsSt.headerTimes.Observe(h, headertime.OriginComet, false)
	}
}

func gatewayHeaderTimes() state.HeaderTimeSource {
	st, err := loadHeightSyncProcessState(nil, effectiveChainRPC(), "")
	if err != nil || st == nil || st.headerTimes == nil {
		return nil
	}
	return st.headerTimes
}

// gatewayHeaderTimeView is the debug snapshot. It does not start the follower.
func gatewayHeaderTimeView() hostevents.HeaderTimeView {
	if hsSt == nil || hsSt.poller == nil {
		return hostevents.HeaderTimeView{}
	}
	return hsSt.poller.HeaderTimeView()
}

func loadHeightSyncProcessState(chainClient *chain.Client, cometRPC, storageDir string) (*heightSyncProcessState, error) {
	hsOnce.Do(func() {
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

		nmOracle, nmClient, nmClose, err := nodeManagerOracleFromEnv()
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
		src, verifier := gatewayHeaderTimeSource(rpc, fo)
		attachGatewayHeaderTimeCache(src, storageDir)
		// Latest header first. StartCometWithHooks subscribes after this returns.
		cometTrusted := seedGatewayMap(context.Background(), verifier, nmOracle, src)
		closer := func() {
			cancelFeed()
			if src != nil {
				_ = src.CloseCache()
			}
			if nmClose != nil {
				nmClose()
			}
		}
		hsSt = &heightSyncProcessState{
			oracle:      fo,
			headerTimes: src,
			verifier:    verifier,
			cometHeader: observeGatewayCometHeader,
			closer:      closer,
		}
		if rpc != "" {
			if err := tipcache.StartCometWithHooks(feedCtx, rpc, cache, tipcache.CometHooks{
				OnConnected:    func() { fo.SetCometConnected(true) },
				OnDisconnected: func() { fo.SetCometConnected(false) },
				OnHeader:       observeGatewayCometHeader,
			}); err != nil {
				slog.Warn("height-sync comet feed", "err", err, "rpc", rpc)
			}
		}
		addr := strings.TrimSpace(os.Getenv("NODE_MANAGER_ADDR"))
		slog.Info(hostevents.TrustedWarmKeyMessage(addr), "node_manager_addr", addr)
		trustDAPI := nmClient != nil && !cometTrusted
		var verify hostevents.BlockVerifier
		if cometTrusted {
			verify = gatewayBlockVerifier(verifier)
		}
		poller := hostevents.NewPoller(hostevents.PollerConfig{
			Config:         hostevents.Config{Client: nmClient, Log: slog.Default()},
			Source:         src,
			Warm:           bridge.WarmKeyCopyFor(chainClient),
			BlocksVerified: trustDAPI,
			StartAtHead:    nmClient != nil,
			Verify:         verify,
		})
		if verifier != nil && verify != nil {
			verifier.SetOnDAPIFailure(poller.DAPIBlockRejected)
		}
		hsSt.poller = poller
		go poller.Run(feedCtx)
		slog.Info("height sync chain follower enabled",
			"node_manager", nmOracle != nil, "direct_chain", chainOracle != nil, "comet_rpc", rpc)
	})
	return hsSt, hsErr
}

func nodeManagerOracleFromEnv() (blocks.BlockOracle, gen.NodeManagerClient, func(), error) {
	addr := strings.TrimSpace(os.Getenv("NODE_MANAGER_ADDR"))
	if addr == "" {
		return nil, nil, nil, nil
	}
	cli, err := nodemanager.NewClient(addr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("chainoracle node-manager dial: %w", err)
	}
	lookup, err := nmclient.New(cli.NodeManagerClient())
	if err != nil {
		_ = cli.Close()
		return nil, nil, nil, err
	}
	return lookup, cli.NodeManagerClient(), func() { _ = cli.Close() }, nil
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
		st, err := loadHeightSyncProcessState(nil, effectiveChainRPC(), "")
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

// heightSyncHeartbeatFromEnv is the gateway quiet-session cadence. Only
// true/1/on (case-insensitive) enable it. Unset, empty, false/0/off, and
// any other value leave heartbeats off. The height-seed loop is separate.
func heightSyncHeartbeatFromEnv() bool {
	return envBoolOptIn(envHeightSyncHeartbeat)
}

// quietSettleDelayFromEnv is how long leftover receipt/Finish wait for a
// successor Start before composing on their own nonce. Unset → production
// default (5m). Testenv sets DEVSHARD_QUIET_SETTLE_DELAY=5s. Invalid or
// non-positive values fall back to the production default.
func quietSettleDelayFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envQuietSettleDelay))
	if raw == "" {
		return user.DefaultQuietSettleDelay
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("invalid quiet settle delay; using default",
			"env", envQuietSettleDelay, "value", raw, "default", user.DefaultQuietSettleDelay, "error", err)
		return user.DefaultQuietSettleDelay
	}
	return d
}

// gatewayChainOracleFromEnv is the optional verification follower. Only
// true/1/on (case-insensitive) enable it. Unset, empty, false/0/off, and
// any other value leave the follower unbuilt.
func gatewayChainOracleFromEnv() bool {
	return envBoolOptIn(envGatewayChainOracle)
}

func envBoolOptIn(name string) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "true", "1", "on":
		return true
	default:
		return false
	}
}
