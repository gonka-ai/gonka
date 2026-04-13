package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/jackc/pgx/v5/pgxpool"

	"common/chain"
	"common/chain/events"
	"subnet/signing"

	"subnet/storage"
)

func main() {
	cfg := Config{
		HTTPAddr:        ":18080",
		ChainGRPCURL:    "localhost:9090",
		ChainRPCURL:     "http://localhost:26657",
		NodeManagerAddr: "localhost:9091",
		SQLitePath:      "",
		PostgresDSN:     "",
		SignerKeyName:   "validator",
		KeyringBackend:  "test",
		KeyringDir:      os.ExpandEnv("$HOME/.gonka"),
		ChainID:         "inference-1",
		InstanceAddress: "",
		PublicURL:       "",
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Chain gRPC client — used by bridge, queryapi, and chain event listener.
	chainClient, err := chain.New(cfg.ChainGRPCURL)
	if err != nil {
		log.Fatalf("chain client: %v", err)
	}

	// Cosmos keyring — source of the secp256k1 signing key.
	kr, err := buildKeyring(cfg)
	if err != nil {
		log.Fatalf("keyring: %v", err)
	}

	// Subnet signer derived from the cosmos keyring key.
	// *signing.Secp256k1Signer satisfies subnet/signing.Signer.
	signer, err := signing.NewSignerFromKeyring(kr, cfg.SignerKeyName)
	if err != nil {
		log.Fatalf("signer: %v", err)
	}

	// SQLite store for session state (group, diffs, signatures).
	sqliteStore, err := storage.NewSQLite(cfg.SQLitePath)
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	defer sqliteStore.Close()

	// PostgreSQL pool — shared by payload store and claims store.
	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	// payloadStore, err := payloads.New(ctx, pool)
	// if err != nil {
	// 	log.Fatalf("payload store: %v", err)
	// }

	// claimStore, err := claims.New(ctx, pool)
	// if err != nil {
	// 	log.Fatalf("claim store: %v", err)
	// }

	// Chain bridge — resolves escrow info and warm keys from chain.
	// TODO: wire a tx.Manager as the Submitter when dispute submission is needed.
	// chainBridge := bridge.NewChainBridge(chainClient, nil)

	// Chain event listener — subscribes to subnet escrow events via CometBFT WebSocket.
	// TODO: wire HandleEscrowCreated / HandleEscrowSettled on hostManager once those methods exist.
	if cfg.ChainRPCURL != "" {
		evListener := events.NewListener(cfg.ChainRPCURL)
		evListener.OnSubnetEscrowCreated(func(ctx context.Context, e events.SubnetEscrowCreatedEvent) {
			log.Printf("subnet_escrow_created: escrow_id=%d creator=%s epoch=%d", e.EscrowID, e.Creator, e.EpochIndex)
		})
		evListener.OnSubnetEscrowSettled(func(ctx context.Context, e events.SubnetEscrowSettledEvent) {
			log.Printf("subnet_escrow_settled: escrow_id=%d settler=%s payout=%d", e.EscrowID, e.Settler, e.TotalPayout)
		})
		go func() {
			if err := evListener.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("chain events listener: %v", err)
			}
		}()
	}

	// TODO: replace with common/subnetengine once implemented.
	// The engine should acquire ML nodes via common/mlnode.NewClient(cfg.NodeManagerAddr),
	// forward requests via common/completionapi, store payloads in payloadStore,
	// and implement subnet.InferenceEngine.
	// engine := stub.NewInferenceEngine()

	// TODO: replace with common/subnetvalidation once implemented.
	// The validator should fetch payloads from the executor, re-run inference,
	// compare logprobs via common/completionapi, and submit MsgValidation.
	// validator := stub.NewValidationEngine()

	// hostManager := manager.New(
	// 	sqliteStore,
	// 	signer,
	// 	engine,
	// 	validator,
	// 	chainBridge,
	// 	payloadStore,
	// 	chainClient.InferenceQueryClient(),
	// )

	// if err := hostManager.RecoverSessions(); err != nil {
	// 	log.Fatalf("recover sessions: %v", err)
	// }

	e := buildServer(chainClient)
	httpSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: e,
	}

	go func() {
		log.Printf("subnetd: listening on %s (signer=%s)", cfg.HTTPAddr, signer.Address())
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http serve: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// buildKeyring creates a cosmos keyring from the given config.
func buildKeyring(cfg Config) (keyring.Keyring, error) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	// nil reader: works for "test" backend; "file" backend reads KEYRING_PASSWORD from env via cosmos SDK.
	kr, err := keyring.New("gonka", cfg.KeyringBackend, cfg.KeyringDir, nil, cdc)
	if err != nil {
		return nil, fmt.Errorf("keyring.New: %w", err)
	}
	return kr, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
