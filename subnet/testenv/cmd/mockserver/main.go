// mockserver is the mock mainnet gRPC server for the subnet testenv.
// It pre-seeds one escrow from the config file and answers MockQuery / MockTx
// calls so that subnethost participants and subnetctl can operate without a
// live Cosmos chain.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"subnet/signing"
	"subnet/state"
	"subnet/types"

	"subnet/testenv/config"
	"subnet/testenv/mockchain"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to testenv config YAML")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	srv := newMockServer(cfg)

	grpcAddr := fmt.Sprintf(":%d", cfg.MockServer.Port)
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", grpcAddr, err)
	}

	gs := grpc.NewServer()
	mockchain.RegisterMockQueryServer(gs, srv)
	mockchain.RegisterMockTxServer(gs, srv)

	// Health/status HTTP endpoint on port+1.
	httpAddr := fmt.Sprintf(":%d", cfg.MockServer.Port+1)
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, "ok")
		})
		mux.HandleFunc("/status", srv.handleStatus)
		log.Printf("mock-server HTTP status on %s", httpAddr)
		if err := http.ListenAndServe(httpAddr, mux); err != nil {
			log.Printf("HTTP status server: %v", err)
		}
	}()

	log.Printf("mock-server gRPC listening on %s (escrow=%s participants=%d slots=%d)",
		grpcAddr, cfg.EscrowID, len(cfg.Participants), cfg.Slots)
	if err := gs.Serve(lis); err != nil {
		log.Fatalf("gRPC serve: %v", err)
	}
}

// ── in-memory server ──────────────────────────────────────────────────────────

type escrowRecord struct {
	info    *mockchain.GetEscrowResponse
	settled bool
}

type participantRecord struct {
	address string
	url     string
}

type mockServer struct {
	mockchain.UnimplementedMockQueryServer
	mockchain.UnimplementedMockTxServer

	mu           sync.RWMutex
	cfg          *config.Config
	escrows      map[string]*escrowRecord     // escrow_id -> record
	participants map[string]*participantRecord // address -> record
	verifier     *signing.Secp256k1Verifier
}

func newMockServer(cfg *config.Config) *mockServer {
	s := &mockServer{
		cfg:          cfg,
		escrows:      make(map[string]*escrowRecord),
		participants: make(map[string]*participantRecord),
		verifier:     signing.NewSecp256k1Verifier(),
	}

	// Pre-populate participant registry.
	for i := range cfg.Participants {
		p := &cfg.Participants[i]
		if p.Address == "" {
			log.Printf("warning: participant %d has no address (run gencompose first)", i)
			continue
		}
		s.participants[p.Address] = &participantRecord{
			address: p.Address,
			url:     p.ParticipantURL(),
		}
	}

	// Pre-seed the escrow so participants can bootstrap immediately.
	s.seedEscrow()

	return s
}

func (s *mockServer) seedEscrow() {
	appHash, err := hex.DecodeString(s.cfg.AppHash)
	if err != nil || len(appHash) == 0 {
		h := [32]byte{}
		copy(h[:], []byte("testenv"))
		appHash = h[:]
	}

	slots := s.cfg.SlotsArray()

	creatorAddr := s.cfg.EffectiveCreatorAddress()
	if creatorAddr == "" {
		// Fallback: warn loudly — subnetctl won't be able to submit inferences
		// because the state machine validates the sender against CreatorAddress.
		// Run `make gen-compose` to populate user.private_key_hex / user.address
		// or set creator_address explicitly.
		log.Printf("WARNING: creator_address and user.address are empty — run `make gen-compose` to fix")
		creatorAddr = "gonka1testenv000000000000000000000000000creator"
	}
	if s.cfg.CreatorAddress != "" && s.cfg.User.Address != "" && s.cfg.CreatorAddress != s.cfg.User.Address {
		log.Printf("NOTICE: creator_address (%s) != user.address (%s) — subnetctl will get 403 unless operator key matches creator_address",
			s.cfg.CreatorAddress, s.cfg.User.Address)
	}

	s.escrows[s.cfg.EscrowID] = &escrowRecord{
		info: &mockchain.GetEscrowResponse{
			EscrowId:       s.cfg.EscrowID,
			Amount:         s.cfg.Amount,
			CreatorAddress: creatorAddr,
			AppHash:        appHash,
			Slots:          slots,
			TokenPrice:     s.cfg.TokenPrice,
		},
	}
	log.Printf("escrow %s seeded: %d slots", s.cfg.EscrowID, len(slots))
}

// ── MockQuery ─────────────────────────────────────────────────────────────────

func (s *mockServer) GetEscrow(_ context.Context, req *mockchain.GetEscrowRequest) (*mockchain.GetEscrowResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.escrows[req.EscrowId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "escrow %s not found", req.EscrowId)
	}
	if rec.settled {
		return nil, status.Errorf(codes.FailedPrecondition, "escrow %s already settled", req.EscrowId)
	}
	return rec.info, nil
}

func (s *mockServer) GetParticipant(_ context.Context, req *mockchain.GetParticipantRequest) (*mockchain.GetParticipantResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.participants[req.Address]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "participant %s not found", req.Address)
	}
	return &mockchain.GetParticipantResponse{
		Address: p.address,
		Url:     p.url,
	}, nil
}

// GetGrantees returns all participant addresses, making every warm-key check pass.
func (s *mockServer) GetGrantees(_ context.Context, _ *mockchain.GetGranteesRequest) (*mockchain.GetGranteesResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	addrs := make([]string, 0, len(s.participants))
	for addr := range s.participants {
		addrs = append(addrs, addr)
	}
	return &mockchain.GetGranteesResponse{Addresses: addrs}, nil
}

// ── MockTx ────────────────────────────────────────────────────────────────────

func (s *mockServer) CreateEscrow(_ context.Context, _ *mockchain.CreateEscrowRequest) (*mockchain.CreateEscrowResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Idempotent: re-seed if not present.
	if _, ok := s.escrows[s.cfg.EscrowID]; !ok {
		s.seedEscrow()
	}
	return &mockchain.CreateEscrowResponse{EscrowId: s.cfg.EscrowID}, nil
}

func (s *mockServer) SettleEscrow(_ context.Context, req *mockchain.SettleEscrowRequest) (*mockchain.SettleEscrowResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.escrows[req.EscrowId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "escrow %s not found", req.EscrowId)
	}
	if rec.settled {
		return &mockchain.SettleEscrowResponse{Ok: true, Message: "already settled"}, nil
	}

	// Rebuild group from the stored escrow slots.
	group := make([]types.SlotAssignment, len(rec.info.Slots))
	for i, addr := range rec.info.Slots {
		group[i] = types.SlotAssignment{SlotID: uint32(i), ValidatorAddress: addr}
	}

	// Convert request payload to state.SettlementPayload for verification.
	payload := state.SettlementPayload{
		EscrowID: req.EscrowId,
		Nonce:    req.Nonce,
		RestHash: req.RestHash,
	}

	payload.HostStats = make(map[uint32]*types.HostStats, len(req.HostStats))
	for _, hs := range req.HostStats {
		payload.HostStats[hs.SlotId] = &types.HostStats{
			Missed:               hs.Missed,
			Invalid:              hs.Invalid,
			Cost:                 hs.Cost,
			RequiredValidations:  hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		}
	}

	payload.Signatures = make(map[uint32][]byte, len(req.Signatures))
	for _, ss := range req.Signatures {
		payload.Signatures[ss.SlotId] = ss.Signature
	}

	_, err := state.VerifySettlement(payload, group, s.verifier, nil)
	if err != nil {
		return &mockchain.SettleEscrowResponse{
			Ok:      false,
			Message: fmt.Sprintf("settlement verification failed: %v", err),
		}, nil
	}

	rec.settled = true
	log.Printf("escrow %s settled at nonce %d", req.EscrowId, req.Nonce)
	return &mockchain.SettleEscrowResponse{Ok: true, Message: "settled"}, nil
}

// ── HTTP status handler ───────────────────────────────────────────────────────

func (s *mockServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fmt.Fprintf(w, "escrows: %d\nparticipants: %d\n", len(s.escrows), len(s.participants))
	for id, rec := range s.escrows {
		fmt.Fprintf(w, "  escrow %s settled=%v slots=%d\n", id, rec.settled, len(rec.info.Slots))
	}
}
