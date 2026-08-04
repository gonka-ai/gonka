package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	devshardpkg "devshard"
	"devshard/gossip"
	"devshard/host"
	"devshard/internal/e2econfig"
	"devshard/observability"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
)

const (
	defaultPort     = "8080"
	defaultEscrowID = "escrow-1"
)

func main() {
	port := flag.String("port", envDefault("DEVSHARD_PORT", defaultPort), "listen port")
	flag.Parse()

	ctx := context.Background()
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	srv, err := buildServer(ctx, cfg)
	if err != nil {
		log.Fatalf("build server: %v", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	registerServer(e.Group(devshardpkg.DefaultRoutePrefix()), srv)

	addr := ":" + *port
	log.Printf("devshard-host listening on %s slot=%d address=%s route_prefix=%s",
		addr, cfg.hostIndex, cfg.signer.Address(), devshardpkg.DefaultRoutePrefix())
	if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// registerServer mounts the transport handlers the same way production
// RegisterLazySessionRoutes does, for a single pre-bound e2e host session.
func registerServer(g *echo.Group, srv *transport.Server) {
	g.Use(observability.EchoMiddleware())
	g.Use(observability.RequestIDMiddleware)

	withAuth := func(recordChatTerminal bool, handler echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			wrapped := srv.RateLimitMiddleware(recordChatTerminal)(handler)
			return srv.AuthMiddleware(wrapped)(c)
		}
	}

	g.POST("/sessions/:id/chat/completions", withAuth(true, srv.HandleInference))
	g.POST("/sessions/:id/verify-timeout", withAuth(false, srv.HandleVerifyTimeout))
	g.POST("/sessions/:id/challenge-receipt", withAuth(false, srv.HandleChallengeReceipt))
	g.POST("/sessions/:id/gossip/nonce", withAuth(false, srv.HandleGossipNonce))
	g.POST("/sessions/:id/gossip/txs", withAuth(false, srv.HandleGossipTxs))
	g.GET("/sessions/:id/diffs", srv.HandleGetDiffs)
	g.GET("/sessions/:id/mempool", srv.HandleGetMempool)
	g.GET("/sessions/:id/signatures", srv.HandleGetSignatures)
}

type hostConfig struct {
	escrowID    string
	hostIndex   int
	signer      *signing.Secp256k1Signer
	userSigner  *signing.Secp256k1Signer
	userAddress string
	group       []types.SlotAssignment
	peerURLs    []string
	dataDir     string
	balance     uint64
	epochID     uint64
	tokenPrice  uint64
	version     string
}

func loadConfig() (hostConfig, error) {
	hostIndex, err := strconv.Atoi(envDefault("DEVSHARD_HOST_INDEX", "0"))
	if err != nil {
		return hostConfig{}, fmt.Errorf("parse DEVSHARD_HOST_INDEX: %w", err)
	}

	hostKeys := splitCSV(os.Getenv("DEVSHARD_HOST_PRIVATE_KEYS"))
	if len(hostKeys) == 0 {
		hostKeys = defaultHostPrivateKeys()
	}
	if hostIndex < 0 || hostIndex >= len(hostKeys) {
		return hostConfig{}, fmt.Errorf("host index %d outside host key count %d", hostIndex, len(hostKeys))
	}

	hostSigner, err := signing.SignerFromHex(envDefault("DEVSHARD_HOST_PRIVATE_KEY", hostKeys[hostIndex]))
	if err != nil {
		return hostConfig{}, fmt.Errorf("host signer: %w", err)
	}

	userKey := envDefault("DEVSHARD_USER_PRIVATE_KEY", defaultUserPrivateKey())
	userSigner, err := signing.SignerFromHex(userKey)
	if err != nil {
		return hostConfig{}, fmt.Errorf("user signer: %w", err)
	}
	userAddress := envDefault("DEVSHARD_USER_ADDRESS", userSigner.Address())

	group, err := groupFromKeys(hostKeys)
	if err != nil {
		return hostConfig{}, err
	}
	peerURLs := splitCSV(os.Getenv("DEVSHARD_PEER_URLS"))
	if len(peerURLs) != len(group) {
		return hostConfig{}, fmt.Errorf("DEVSHARD_PEER_URLS count %d must match group size %d", len(peerURLs), len(group))
	}

	return hostConfig{
		escrowID:    envDefault("DEVSHARD_ESCROW_ID", defaultEscrowID),
		hostIndex:   hostIndex,
		signer:      hostSigner,
		userSigner:  userSigner,
		userAddress: userAddress,
		group:       group,
		peerURLs:    peerURLs,
		dataDir:     envDefault("DEVSHARD_DATA_DIR", filepath.Join(os.TempDir(), "devshard-host")),
		balance:     uintEnv("DEVSHARD_ESCROW_AMOUNT", 1_000_000),
		epochID:     uintEnv("DEVSHARD_EPOCH_ID", 1),
		tokenPrice:  uintEnv("DEVSHARD_TOKEN_PRICE", 1),
		version:     envDefault("DEVSHARD_VERSION", types.EffectiveStateRootAndProtocolVersion),
	}, nil
}

func buildServer(ctx context.Context, cfg hostConfig) (*transport.Server, error) {
	verifier := signing.NewSecp256k1Verifier()
	sessionConfig, err := sessionConfigFromEnv(len(cfg.group))
	if err != nil {
		return nil, err
	}

	store, err := storage.NewStorage(ctx, cfg.dataDir)
	if err != nil {
		return nil, err
	}

	sm, err := state.NewStateMachine(
		cfg.escrowID,
		sessionConfig,
		cfg.group,
		cfg.balance,
		cfg.userAddress,
		verifier,
		store,
		state.WithVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	if err := store.CreateSession(storage.CreateSessionParams{
		EscrowID:       cfg.escrowID,
		EpochID:        cfg.epochID,
		Version:        cfg.version,
		CreatorAddr:    cfg.userAddress,
		Config:         sessionConfig,
		Group:          cfg.group,
		InitialBalance: cfg.balance,
	}); err != nil {
		return nil, err
	}
	if err := recoverHostState(store, sm, cfg.escrowID); err != nil {
		return nil, err
	}

	inferenceEngine := devshardpkg.InferenceEngine(stub.NewInferenceEngine())
	if delay := stubInferenceDelayFromEnv(); delay > 0 {
		inferenceEngine = delayedInferenceEngine{inner: inferenceEngine, delay: delay}
	}

	h, err := host.NewHost(
		sm,
		cfg.signer,
		inferenceEngine,
		cfg.escrowID,
		cfg.group,
		nil,
		host.WithStorage(store),
		host.WithEpochID(cfg.epochID),
		host.WithVerifier(verifier),
		host.WithValidator(stub.NewValidationEngine()),
	)
	if err != nil {
		return nil, err
	}
	h.Start()

	srv, err := transport.NewServer(h, store, verifier, cfg.userAddress)
	if err != nil {
		return nil, err
	}

	userPeers := make(map[int]*transport.HTTPClient, len(cfg.peerURLs))
	var gossipPeers []gossip.PeerClient
	for i, peerURL := range cfg.peerURLs {
		userPeers[i] = transport.NewHTTPClient(peerURL, cfg.escrowID, cfg.userSigner)
		if i != cfg.hostIndex {
			gossipPeers = append(gossipPeers, transport.NewHTTPClient(peerURL, cfg.escrowID, cfg.signer))
		}
	}
	srv.SetPeerClients(userPeers)
	srv.SetGossip(gossip.NewGossip(cfg.escrowID, uint32(cfg.hostIndex), gossipPeers, h.HostMempool(), gossip.WithSigAccumulator(h)))

	return srv, nil
}

func recoverHostState(store storage.Storage, sm *state.StateMachine, escrowID string) error {
	meta, err := store.GetSessionMeta(escrowID)
	if err != nil {
		return fmt.Errorf("get session meta: %w", err)
	}
	if meta.LatestNonce == 0 {
		return nil
	}

	replayFrom := uint64(1)
	if snapNonce, snapData, snapErr := store.LoadSnapshot(escrowID); snapErr == nil && snapNonce > 0 && snapNonce <= meta.LatestNonce {
		snapState, err := host.UnmarshalStateSnapshot(snapData)
		if err != nil {
			return fmt.Errorf("unmarshal snapshot nonce %d: %w", snapNonce, err)
		}
		sm.RestoreState(snapState)
		replayFrom = snapNonce + 1
	} else if snapErr != nil && !errors.Is(snapErr, storage.ErrSnapshotNotFound) {
		return fmt.Errorf("load snapshot: %w", snapErr)
	}

	if replayFrom > meta.LatestNonce {
		return nil
	}

	records, err := store.GetDiffs(escrowID, replayFrom, meta.LatestNonce)
	if err != nil {
		return fmt.Errorf("get diffs %d..%d: %w", replayFrom, meta.LatestNonce, err)
	}
	for _, rec := range records {
		sm.InjectWarmKeys(rec.WarmKeyDelta)
		root, err := sm.ApplyDiff(rec.Diff)
		if err != nil {
			return fmt.Errorf("replay nonce %d: %w", rec.Nonce, err)
		}
		if len(rec.StateHash) > 0 && len(root) > 0 && !bytes.Equal(root, rec.StateHash) {
			return fmt.Errorf("state root mismatch at nonce %d", rec.Nonce)
		}
	}
	return nil
}

type delayedInferenceEngine struct {
	inner devshardpkg.InferenceEngine
	delay time.Duration
}

func (e delayedInferenceEngine) Execute(ctx context.Context, req devshardpkg.ExecuteRequest) (*devshardpkg.ExecuteResult, error) {
	timer := time.NewTimer(e.delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return e.inner.Execute(ctx, req)
	}
}

// sessionConfigFromEnv mirrors bridge.SessionConfigAtBind so e2e hosts stay
// aligned with devshardctl when escrow params come from mock-chain gRPC.
func sessionConfigFromEnv(groupSize int) (types.SessionConfig, error) {
	cfg := types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{
		TokenPrice:                uintEnv("DEVSHARD_TOKEN_PRICE", 1),
		CreateDevshardFee:         uintEnv("DEVSHARD_CREATE_DEVSHARD_FEE", 0),
		FeePerNonce:               uintEnv("DEVSHARD_FEE_PER_NONCE", 0),
		InferenceSealGraceNonces:  uint32(uintEnv("DEVSHARD_INFERENCE_SEAL_GRACE_NONCES", 0)),
		InferenceSealGraceSeconds: uint32(uintEnv("DEVSHARD_INFERENCE_SEAL_GRACE_SECONDS", 0)),
		AutoSealEveryNNonces:      uint32(uintEnv("DEVSHARD_AUTO_SEAL_EVERY_N_NONCES", 0)),
		ValidationRate:            uint32(uintEnv("DEVSHARD_VALIDATION_RATE", 0)),
		VoteThresholdFactor:       uint32(uintEnv("DEVSHARD_VOTE_THRESHOLD_FACTOR", 0)),
	})
	overrides, err := e2econfig.SessionTimeoutOverridesFromEnv()
	if err != nil {
		return types.SessionConfig{}, err
	}
	cfg = overrides.Apply(cfg)
	return types.NormalizeSessionConfig(cfg, groupSize), nil
}

func groupFromKeys(keys []string) ([]types.SlotAssignment, error) {
	group := make([]types.SlotAssignment, len(keys))
	for i, key := range keys {
		signer, err := signing.SignerFromHex(key)
		if err != nil {
			return nil, fmt.Errorf("host key %d: %w", i, err)
		}
		group[i] = types.SlotAssignment{SlotID: uint32(i), ValidatorAddress: signer.Address()}
	}
	return group, nil
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func uintEnv(key string, fallback uint64) uint64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return value
}

func durationMillisEnv(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	if value < 0 {
		log.Fatalf("invalid %s: must be non-negative", key)
	}
	return time.Duration(value) * time.Millisecond
}

func stubInferenceDelayFromEnv() time.Duration {
	delay := durationMillisEnv("DEVSHARD_STUB_INFERENCE_DELAY_MS", 0)
	if delay == 0 {
		return 0
	}
	if os.Getenv("DEVSHARD_E2E") != "1" {
		log.Fatalf("DEVSHARD_STUB_INFERENCE_DELAY_MS is only supported when DEVSHARD_E2E=1")
	}
	return delay
}

func defaultHostPrivateKeys() []string {
	return []string{
		"0000000000000000000000000000000000000000000000000000000000000011",
		"0000000000000000000000000000000000000000000000000000000000000012",
		"0000000000000000000000000000000000000000000000000000000000000013",
	}
}

func defaultUserPrivateKey() string {
	return "0000000000000000000000000000000000000000000000000000000000000021"
}
