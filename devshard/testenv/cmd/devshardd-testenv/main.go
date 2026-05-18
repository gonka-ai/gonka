// Binary devshardd-testenv is the testenv-specific devshardd host
// process. One container per escrow-host pair. It replaces the
// production devshardd binary (which lives in decentralized-api and
// depends on the real chain keyring, NodeManager broker, and ML
// payload store) with a self-contained wiring that matches what
// testenv.md §5 prescribes:
//
//   - hex signer (no keyring);
//   - testenv bridge speaking gRPC to the mock-chain container;
//   - mockdapi library providing a host-trust BlockOracle and a
//     no-op NodeManager;
//   - stub inference / validation engines (env-var configurable);
//   - single devshard.Host per process (per-escrow).
//
// Everything else is plumbed via env vars documented in §4.2 of
// testenv.md. This binary MUST NOT import anything under
// decentralized-api: it is the only point where the testenv wiring
// would be tempted to reach back into dapi, so we enforce the
// boundary at the command level.
//
// See devshard/docs/testenv.md §Phase 8.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"devshard/bridge"
	"devshard/gossip"
	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	testenvbridge "devshard/testenv/bridge"
	testenvengine "devshard/testenv/engine"
	"devshard/testenv/mockdapi"
	"devshard/testenv/obsmetrics"
	"devshard/transport"
	"devshard/types"
)

// envConfig is the resolved configuration for a single devshardd-testenv
// process. Every field maps 1:1 to an env var (or its flag override) in
// §4.2 of testenv.md — if you add a field here, update that table.
type envConfig struct {
	PrivateKeyHex string
	EscrowID      string
	MockChainURL  string
	HeightSyncURL string
	ChainID       string
	HTTPPort      int
	DataDir       string
}

// loadEnvConfig reads the env-var contract and returns a fully-formed
// config. Missing required fields return a composite error so misconfig
// is surfaced in one log line rather than killed serially.
func loadEnvConfig() (envConfig, error) {
	cfg := envConfig{
		PrivateKeyHex: os.Getenv("TESTENV_PRIVATE_KEY"),
		EscrowID:      os.Getenv("ESCROW_ID"),
		MockChainURL:  os.Getenv("MOCK_CHAIN_URL"),
		HeightSyncURL: os.Getenv("HEIGHT_SYNC_URL"),
		ChainID:       os.Getenv("CHAIN_ID"),
		DataDir:       envOr("DATA_DIR", "/data"),
	}

	httpPortStr := envOr("HTTP_PORT", "9500")
	if p, err := strconv.Atoi(httpPortStr); err == nil && p > 0 {
		cfg.HTTPPort = p
	} else {
		return cfg, fmt.Errorf("invalid HTTP_PORT %q: %w", httpPortStr, err)
	}

	var missing []string
	if cfg.PrivateKeyHex == "" {
		missing = append(missing, "TESTENV_PRIVATE_KEY")
	}
	if cfg.EscrowID == "" {
		missing = append(missing, "ESCROW_ID")
	}
	if cfg.MockChainURL == "" {
		missing = append(missing, "MOCK_CHAIN_URL")
	}
	if cfg.HeightSyncURL == "" {
		missing = append(missing, "HEIGHT_SYNC_URL")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("required env vars missing: %v", missing)
	}
	return cfg, nil
}

func main() {
	// Flags exist only so local `go run` invocations don't need to
	// export the env-var set. Env vars take precedence when both are
	// set to keep container behavior deterministic.
	_ = flag.String("private-key", "", "hex private key (overrides TESTENV_PRIVATE_KEY)")
	_ = flag.String("escrow-id", "", "escrow ID (overrides ESCROW_ID)")
	flag.Parse()

	logging.ConfigureSlogFromEnv()

	cfg, err := loadEnvConfig()
	if err != nil {
		slog.Error("devshardd-testenv: config", "error", err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		slog.Error("devshardd-testenv: fatal", "error", err)
		os.Exit(1)
	}
}

func run(cfg envConfig) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}

	signer, err := signing.SignerFromHex(cfg.PrivateKeyHex)
	if err != nil {
		return fmt.Errorf("signer from hex: %w", err)
	}
	verifier := signing.NewSecp256k1Verifier()
	myAddr := signer.Address()

	logging.Info("devshardd-testenv starting",
		"subsystem", "devshardd-testenv",
		"escrow", cfg.EscrowID,
		"address", myAddr,
		"mock_chain", cfg.MockChainURL,
		"height_sync", cfg.HeightSyncURL,
		"http_port", cfg.HTTPPort,
	)

	br, err := testenvbridge.NewGRPCBridge(ctx, cfg.MockChainURL)
	if err != nil {
		return fmt.Errorf("create gRPC bridge: %w", err)
	}
	defer func() {
		if cerr := br.Close(); cerr != nil {
			logging.Warn("close gRPC bridge", "subsystem", "devshardd-testenv", "error", cerr)
		}
	}()

	md, err := mockdapi.New(ctx, mockdapi.Config{
		HeightSyncURL: cfg.HeightSyncURL,
		ChainID:       cfg.ChainID,
		StaleAfter:    mockdapi.StaleAfterFromEnv(),
	})
	if err != nil {
		return fmt.Errorf("mockdapi.New: %w", err)
	}
	defer md.Close()

	storePath := filepath.Join(cfg.DataDir, "devshardd.db")
	store, err := storage.NewSQLite(storePath)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", storePath, err)
	}
	defer store.Close()

	group, err := bridge.BuildGroup(cfg.EscrowID, br)
	if err != nil {
		return fmt.Errorf("build group: %w", err)
	}
	escrow, err := br.GetEscrow(cfg.EscrowID)
	if err != nil {
		return fmt.Errorf("get escrow: %w", err)
	}

	sessionCfg := types.SessionConfigWithPrice(len(group), escrow.TokenPrice)

	// Create-session is best-effort: a stale sqlite from a previous
	// run must not kill the process, it just means we're recovering.
	if err := store.CreateSession(storage.CreateSessionParams{
		EscrowID:       cfg.EscrowID,
		CreatorAddr:    escrow.CreatorAddress,
		Config:         sessionCfg,
		Group:          group,
		InitialBalance: escrow.Amount,
	}); err != nil {
		logging.Info("session already exists, continuing",
			"subsystem", "devshardd-testenv",
			"escrow", cfg.EscrowID,
			"error", err)
	}

	sm, err := state.NewStateMachine(
		cfg.EscrowID, sessionCfg, group, escrow.Amount, escrow.CreatorAddress, verifier,
		state.WithWarmKeyResolver(br.VerifyWarmKey),
	)
	if err != nil {
		return fmt.Errorf("new state machine: %w", err)
	}

	inference := testenvengine.NewMockInferenceFromEnv()
	validator := testenvengine.NewMockValidationFromEnv()

	h, err := host.NewHost(
		sm, signer, inference, cfg.EscrowID, group, nil,
		host.WithStorage(store),
		host.WithVerifier(verifier),
		host.WithValidator(validator),
		host.WithBlockOracle(md.Oracle),
	)
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}
	// md.NodeManager is deliberately unused by the stub engines — the
	// NoopNodeManager exists so downstream production wiring swaps in
	// a real NodeManager gRPC client without shape changes. Pinning
	// it to _ documents the intentional gap.
	_ = md.NodeManager

	// Gossip wiring — peer clients authenticated with OUR signer so
	// the remote side can match us against the escrow slot list.
	peerClients, err := buildPeerClients(cfg.EscrowID, myAddr, group, signer, br)
	if err != nil {
		return fmt.Errorf("build peer clients: %w", err)
	}
	peers := make([]gossip.PeerClient, len(peerClients))
	for i, c := range peerClients {
		peers[i] = c
	}
	var gopts []gossip.GossipOption
	gopts = append(gopts, gossip.WithSigAccumulator(h))
	if len(peerClients) > 0 {
		gopts = append(gopts, gossip.WithRecovery(peerClients[0], h))
	}

	// Primary slot ID = first slot owned by this host, or 0 if this
	// process doesn't own a slot (shouldn't happen in a healthy
	// testenv — the escrow lists us — but bounds-check anyway).
	primarySlot := primarySlotID(group, myAddr)

	g := gossip.NewGossip(cfg.EscrowID, primarySlot, peers, h.HostMempool(), gopts...)
	g.Start(ctx)
	defer func() {
		logging.Info("stopping gossip",
			"subsystem", "devshardd-testenv",
			"escrow", cfg.EscrowID,
			"address", myAddr)
		g.Stop()
	}()

	// K / slots: docker-compose sets HEIGHT_SYNC_* from testenv config.yaml
	// (gencompose). Precedence: env → defaults (K=10, slots=len(group));
	// if anchor K is not set via env and K < slots, K is raised to slots so
	// heightsync.NewAnchorScheduler never sees K < SlotsNum (e.g. 16 escrow
	// slots vs legacy default K=10).
	anchorK := uint64(10)
	anchorKFromEnv := false
	if v := os.Getenv("HEIGHT_SYNC_ANCHOR_PERIOD_NONCES"); v != "" {
		anchorKFromEnv = true
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			anchorK = n
		}
	}
	slots := uint64(len(group))
	if slots == 0 {
		slots = 1
	}
	if v := os.Getenv("HEIGHT_SYNC_SYNC_TURN_SLOTS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			slots = n
		}
	}
	if !anchorKFromEnv && anchorK < slots {
		anchorK = slots
	}
	anchorSched, err := heightsync.NewAnchorSchedulerFromOracle(anchorK, slots, md.Oracle)
	if err != nil {
		return fmt.Errorf("height sync anchor scheduler: %w", err)
	}

	srv, err := transport.NewServer(h, store, verifier, escrow.CreatorAddress,
		transport.WithBridge(br),
		transport.WithHeightSync(anchorSched, md.Oracle),
	)
	if err != nil {
		return fmt.Errorf("create transport server: %w", err)
	}
	srv.SetGossip(g)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())
	var obs *obsmetrics.Metrics
	if os.Getenv("EXPORT_METRICS") == "1" {
		om, errM := obsmetrics.New()
		if errM != nil {
			return fmt.Errorf("init observability metrics: %w", errM)
		}
		obs = om
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				obs.ObserveInbound(obsmetrics.ClassifyEchoPath(c.Request().URL.Path))
				return next(c)
			}
		})
	}
	if obs != nil {
		mp := envOr("METRICS_PORT", "9600")
		if err := startMetricsAndSampler(ctx, obs, h, mp); err != nil {
			return err
		}
	}

	srv.Register(e.Group("/v1/devshard"))
	registerHostInferenceHoldDebugRoutes(e, srv)
	e.GET("/healthz", func(c echo.Context) error {
		return c.String(http.StatusOK,
			fmt.Sprintf("devshardd-testenv ok escrow=%s address=%s",
				cfg.EscrowID, myAddr))
	})

	addr := fmt.Sprintf(":%d", cfg.HTTPPort)
	errCh := make(chan error, 1)
	go func() {
		logging.Info("HTTP listening",
			"subsystem", "devshardd-testenv",
			"addr", addr)
		if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logging.Info("shutdown signal received", "subsystem", "devshardd-testenv")
	case err, ok := <-errCh:
		if ok && err != nil {
			return fmt.Errorf("HTTP server: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = e.Shutdown(shutdownCtx)
	logging.Info("devshardd-testenv stopped", "subsystem", "devshardd-testenv")
	return nil
}

// startMetricsAndSampler exposes /metrics (METRICS_PORT) and a periodic sampler for
// gauges. Shuts the metrics HTTP server when ctx is cancelled.
func startMetricsAndSampler(
	ctx context.Context,
	obs *obsmetrics.Metrics,
	h *host.Host,
	metricsPort string,
) error {
	port, err := strconv.Atoi(metricsPort)
	if err != nil {
		return fmt.Errorf("invalid METRICS_PORT %q: %w", metricsPort, err)
	}
	if port <= 0 {
		return fmt.Errorf("invalid METRICS_PORT %d: must be > 0", port)
	}
	addr := fmt.Sprintf(":%d", port)
	s := &http.Server{
		Addr:              addr,
		Handler:           obs.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if errL := s.ListenAndServe(); errL != nil && !errors.Is(errL, http.ErrServerClosed) {
			logging.Error("metrics server", "addr", addr, "error", errL)
		}
	}()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				height, errH := h.LatestHeight(cctx)
				cancel()
				var hi int64
				if errH == nil {
					hi = height
				}
				n := h.LatestNonce()
				pending := len(h.MempoolTxs())
				obs.SetHostState(n, hi, pending)
				errStr := ""
				if errH != nil {
					errStr = errH.Error()
				}
				logging.Debug("metrics_sampler",
					"subsystem", "metrics-sampler",
					"latest_nonce", n,
					"oracle_height", hi,
					"oracle_height_query_err", errStr,
					"pending_mempool", pending,
				)
			}
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(sctx)
	}()
	logging.Info("Prometheus /metrics",
		"subsystem", "devshardd-testenv", "addr", addr)
	return nil
}

// buildPeerClients resolves every other validator address in the
// escrow to an authenticated transport client. Each client signs with
// cfg.signer so the remote host can match us against its escrow slot
// list via the shared auth middleware.
func buildPeerClients(
	escrowID, myAddr string,
	group []types.SlotAssignment,
	signer signing.Signer,
	br bridge.MainnetBridge,
) ([]*transport.HTTPClient, error) {
	seen := make(map[string]bool)
	var peerAddrs []string
	for _, s := range group {
		if s.ValidatorAddress == myAddr || seen[s.ValidatorAddress] {
			continue
		}
		seen[s.ValidatorAddress] = true
		peerAddrs = append(peerAddrs, s.ValidatorAddress)
	}

	var clients []*transport.HTTPClient
	for _, addr := range peerAddrs {
		info, err := br.GetHostInfo(addr)
		if err != nil {
			return nil, fmt.Errorf("get host info %s: %w", addr, err)
		}
		clients = append(clients, transport.NewHTTPClient(info.URL, escrowID, signer))
	}
	return clients, nil
}

// primarySlotID returns the first slot ID this host owns, or 0 if
// none. Gossip uses this as the sender slot when fanning out
// signatures; a host with multiple slots still has one canonical
// primary. 0 is a valid slot; the "not found" case is defensive only.
func primarySlotID(group []types.SlotAssignment, addr string) uint32 {
	for _, s := range group {
		if s.ValidatorAddress == addr {
			return s.SlotID
		}
	}
	return 0
}

// envOr returns os.Getenv(key) if non-empty, else fallback. Kept as a
// tiny helper to match the subnet-testenv style and keep loadEnvConfig
// readable.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
