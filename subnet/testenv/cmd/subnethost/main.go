// subnethost is a standalone subnet participant binary for the testenv.
// Each container in the docker-compose network runs one instance of this binary.
// It replaces decentralized-api for testing purposes, using a stub inference
// engine and a gRPC bridge to the mock mainnet server.
//
// Logging levels:
//   INFO  – lifecycle events (start, gossip init, shutdown)
//   DEBUG – internal gossip fan-out, recovery, sig accumulation (from gossip package)
//   TRACE – per-peer resolution during gossip wiring
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	subnetbridge "subnet/bridge"
	"subnet/gossip"
	"subnet/host"
	"subnet/logging"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/transport"
	"subnet/types"

	testenvbridge "subnet/testenv/bridge"
)

// Trace is a custom slog level below Debug (-4), used for high-frequency
// per-peer events during gossip wiring that would otherwise flood debug output.
const Trace = slog.Level(-8)

// initLogging configures slog to show TRACE and above, with a human-readable
// label for the custom level.
func initLogging() {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: Trace,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				if lv, ok := a.Value.Any().(slog.Level); ok && lv == Trace {
					a.Value = slog.StringValue("TRACE")
				}
			}
			return a
		},
	})
	slog.SetDefault(slog.New(h))
}

func trace(msg string, args ...any) {
	slog.Log(context.Background(), Trace, msg, args...)
}

func main() {
	initLogging()

	privateKey := flag.String("private-key", "", "secp256k1 private key hex (or TESTENV_PRIVATE_KEY)")
	escrowID := flag.String("escrow-id", "", "escrow ID (or TESTENV_ESCROW_ID)")
	mockServer := flag.String("mock-server", "", "mock gRPC server address, e.g. mock-server:9090 (or TESTENV_MOCK_SERVER)")
	port := flag.String("port", "8080", "listen port (or TESTENV_PORT)")
	storagePath := flag.String("storage-path", "", "SQLite DB path for crash recovery (or TESTENV_STORAGE_PATH)")
	flag.Parse()

	keyHex := envOr(*privateKey, "TESTENV_PRIVATE_KEY")
	if keyHex == "" {
		log.Fatal("--private-key or TESTENV_PRIVATE_KEY required")
	}
	eid := envOr(*escrowID, "TESTENV_ESCROW_ID")
	if eid == "" {
		log.Fatal("--escrow-id or TESTENV_ESCROW_ID required")
	}
	mockAddr := envOr(*mockServer, "TESTENV_MOCK_SERVER")
	if mockAddr == "" {
		log.Fatal("--mock-server or TESTENV_MOCK_SERVER required")
	}

	if err := run(keyHex, eid, mockAddr, envOr(*port, "TESTENV_PORT"), envOr(*storagePath, "TESTENV_STORAGE_PATH")); err != nil {
		log.Fatal(err)
	}
}

func run(keyHex, eid, mockAddr, listenPort, dbPath string) error {
	// ── context + signal handling ─────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		logging.Info("shutdown signal received", "subsystem", "subnethost", "signal", sig.String())
		cancel()
	}()

	// ── bridge ────────────────────────────────────────────────────────────────
	br, err := testenvbridge.NewGRPCBridge(mockAddr)
	if err != nil {
		return fmt.Errorf("create gRPC bridge: %w", err)
	}

	// ── signer ────────────────────────────────────────────────────────────────
	signer, err := signing.SignerFromHex(keyHex)
	if err != nil {
		return fmt.Errorf("create signer: %w", err)
	}
	verifier := signing.NewSecp256k1Verifier()
	myAddr := signer.Address()

	logging.Info("subnethost starting",
		"subsystem", "subnethost",
		"escrow", eid,
		"address", myAddr,
		"mock_server", mockAddr,
	)

	// ── storage ───────────────────────────────────────────────────────────────
	var store storage.Storage
	if dbPath != "" {
		store, err = storage.NewSQLite(dbPath)
		if err != nil {
			return fmt.Errorf("create sqlite storage: %w", err)
		}
	} else {
		store = storage.NewMemory()
	}
	defer store.Close()

	// ── group + escrow ────────────────────────────────────────────────────────
	group, err := subnetbridge.BuildGroup(eid, br)
	if err != nil {
		return fmt.Errorf("build group: %w", err)
	}

	escrow, err := br.GetEscrow(eid)
	if err != nil {
		return fmt.Errorf("get escrow: %w", err)
	}

	sessionCfg := types.SessionConfigWithPrice(len(group), escrow.TokenPrice)

	if err := store.CreateSession(storage.CreateSessionParams{
		EscrowID:       eid,
		CreatorAddr:    escrow.CreatorAddress,
		Config:         sessionCfg,
		Group:          group,
		InitialBalance: escrow.Amount,
	}); err != nil {
		// May already exist from a previous run — not fatal.
		logging.Info("session already exists, continuing", "subsystem", "subnethost", "err", err)
	}

	// ── state machine ─────────────────────────────────────────────────────────
	sm := state.NewStateMachine(
		eid, sessionCfg, group, escrow.Amount, escrow.CreatorAddress, verifier,
		state.WithWarmKeyResolver(br.VerifyWarmKey),
	)

	// ── inference / validation engines ───────────────────────────────────────
	engine := NewMockInferenceEngine()
	validator := NewMockValidationEngine()

	// ── host ──────────────────────────────────────────────────────────────────
	// Created before gossip so the host can be passed to NewGossip as
	// SigAccumulator and StateUpdater (gossip.WithRecovery).
	h, err := host.NewHost(sm, signer, engine, eid, group, nil,
		host.WithValidator(validator),
		host.WithStorage(store),
	)
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}

	// ── gossip ────────────────────────────────────────────────────────────────
	g, err := wireGossip(eid, myAddr, group, signer, h, br)
	if err != nil {
		return fmt.Errorf("wire gossip: %w", err)
	}

	g.Start(ctx)
	defer func() {
		logging.Info("stopping gossip", "subsystem", "gossip", "escrow", eid, "address", myAddr)
		g.Stop()
		logging.Info("gossip stopped", "subsystem", "gossip", "escrow", eid, "address", myAddr)
	}()

	// ── transport server ──────────────────────────────────────────────────────
	srv, err := transport.NewServer(h, store, verifier, escrow.CreatorAddress,
		transport.WithBridge(br),
	)
	if err != nil {
		return fmt.Errorf("create transport server: %w", err)
	}

	// Attach gossip so the server forwards incoming nonce/tx messages to the
	// Gossip instance, which then fans them out to K random peers.
	srv.SetGossip(g)

	// ── HTTP server via Echo ──────────────────────────────────────────────────
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	subnetGroup := e.Group("/v1/subnet")
	srv.Register(subnetGroup)

	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK,
			fmt.Sprintf("subnethost ok escrow=%s address=%s", eid, myAddr))
	})

	// Shut down Echo when the context is cancelled (SIGTERM/SIGINT).
	go func() {
		<-ctx.Done()
		logging.Info("shutting down HTTP server", "subsystem", "subnethost")
		_ = e.Shutdown(context.Background())
	}()

	addr := ":" + listenPort
	logging.Info("subnethost HTTP listening", "subsystem", "subnethost", "addr", addr)

	if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("HTTP server: %w", err)
	}
	return nil
}

// wireGossip resolves peer URLs from the bridge, builds transport.HTTPClient
// instances for each peer, and delegates construction to h.CreateGossip which
// wires escrow ID, primary slot ID, mempool, and SigAccumulator automatically.
//
// Logging:
//   TRACE – per-peer address resolution
//   INFO  – summary after wiring completes
func wireGossip(
	eid, myAddr string,
	group []types.SlotAssignment,
	signer signing.Signer,
	h *host.Host,
	br subnetbridge.MainnetBridge,
) (*gossip.Gossip, error) {
	// Collect unique peer validator addresses, excluding our own.
	seen := make(map[string]bool)
	var peerAddrs []string
	for _, slot := range group {
		addr := slot.ValidatorAddress
		if addr == myAddr || seen[addr] {
			continue
		}
		seen[addr] = true
		peerAddrs = append(peerAddrs, addr)
	}

	// Resolve each address to a URL and create an authenticated HTTPClient.
	// Peer clients use OUR signer so the remote server can authenticate us as a
	// group member (our address is in the escrow slot list).
	var peerClients []*transport.HTTPClient
	for _, addr := range peerAddrs {
		trace("resolving gossip peer",
			"subsystem", "gossip",
			"escrow", eid,
			"peer_address", addr,
		)
		info, err := br.GetHostInfo(addr)
		if err != nil {
			return nil, fmt.Errorf("GetHostInfo(%s): %w", addr, err)
		}
		trace("gossip peer resolved",
			"subsystem", "gossip",
			"escrow", eid,
			"peer_address", addr,
			"peer_url", info.URL,
		)
		peerClients = append(peerClients, transport.NewHTTPClient(info.URL, eid, signer))
	}

	peers := make([]gossip.PeerClient, len(peerClients))
	for i, c := range peerClients {
		peers[i] = c
	}

	// Use the first peer's HTTPClient as the recovery diff fetcher.
	// On recovery the gossip layer pulls missing diffs from this peer and
	// replays them through the host.
	var opts []gossip.GossipOption
	recoveryEnabled := len(peerClients) > 0
	if recoveryEnabled {
		opts = append(opts, gossip.WithRecovery(peerClients[0], h))
	}

	// CreateGossip wires escrow ID, primary slot ID, mempool, and
	// SigAccumulator automatically and stores g on h so seed-reveal
	// eager-broadcasts fire correctly.
	g := h.CreateGossip(peers, opts...)

	logging.Info("gossip initialized",
		"subsystem", "gossip",
		"escrow", eid,
		"address", myAddr,
		"peer_count", len(peers),
		"peer_addresses", peerAddrs,
		"recovery_enabled", recoveryEnabled,
	)

	return g, nil
}

// envOr returns flagVal if non-empty, else the environment variable.
func envOr(flagVal, envKey string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envKey)
}
