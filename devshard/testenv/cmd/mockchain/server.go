package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"devshard/signing"
	"devshard/state"
	"devshard/testenv/config"
	"devshard/testenv/proto/mockchainpb"
	"devshard/types"
)

// mockServer serves the MockChain gRPC service against in-memory state
// seeded from the testenv config. It is concurrency-safe; every field
// mutation goes through the mu RWMutex.
type mockServer struct {
	mockchainpb.UnimplementedMockChainServer

	mu           sync.RWMutex
	cfg          *config.Config
	escrows      map[string]*escrowRecord
	participants map[string]*participantRecord
	verifier     signing.Verifier
}

type escrowRecord struct {
	info    *mockchainpb.GetDevshardEscrowResponse
	version string
	group   []types.SlotAssignment
	settled bool
}

type participantRecord struct {
	address string
	url     string
}

// newMockServer constructs and pre-seeds the in-memory state.
func newMockServer(cfg *config.Config) *mockServer {
	s := &mockServer{
		cfg:          cfg,
		escrows:      make(map[string]*escrowRecord),
		participants: make(map[string]*participantRecord),
		verifier:     signing.NewSecp256k1Verifier(),
	}
	s.registerParticipants()
	s.seedEscrow()
	return s
}

// registerParticipants walks cfg.Hosts and populates the participant
// lookup used by GetParticipant / GetGrantees.
func (s *mockServer) registerParticipants() {
	for i := range s.cfg.Hosts {
		h := &s.cfg.Hosts[i]
		if h.Address == "" {
			log.Printf("warning: host[%d] id=%q has no address (run gencompose first)", i, h.ID)
			continue
		}
		s.participants[h.Address] = &participantRecord{
			address: h.Address,
			url:     h.HostURL(),
		}
	}
}

func (s *mockServer) seedEscrow() {
	appHash := s.cfg.MustAppHash()
	if len(appHash) == 0 {
		appHash = []byte("testenv")
	}
	slots := s.cfg.SlotsArray()
	creator := s.cfg.EffectiveCreatorAddress()
	if creator == "" {
		log.Printf("WARNING: escrow.creator_address and user.address are both empty — run `make gen-compose` to fix")
		creator = "gonka1testenv000000000000000000000000000creator"
	}

	s.escrows[s.cfg.Escrow.ID] = &escrowRecord{
		info: &mockchainpb.GetDevshardEscrowResponse{
			EscrowId:       s.cfg.Escrow.ID,
			Amount:         s.cfg.Escrow.Amount,
			CreatorAddress: creator,
			AppHash:        appHash,
			Slots:          slots,
			TokenPrice:     s.cfg.Escrow.TokenPrice,
		},
		version: s.cfg.Escrow.Version,
		group:   buildGroup(slots),
	}
	log.Printf("escrow %s seeded: %d slots version=%q", s.cfg.Escrow.ID, len(slots), s.cfg.Escrow.Version)
}

// buildGroup turns a slots array into the []SlotAssignment shape that
// state.VerifySettlement expects. The slot id is the array index.
func buildGroup(slots []string) []types.SlotAssignment {
	g := make([]types.SlotAssignment, len(slots))
	for i, addr := range slots {
		g[i] = types.SlotAssignment{SlotID: uint32(i), ValidatorAddress: addr}
	}
	return g
}

// ── MockChain queries ─────────────────────────────────────────────────────────

func (s *mockServer) GetDevshardEscrow(_ context.Context, req *mockchainpb.GetDevshardEscrowRequest) (*mockchainpb.GetDevshardEscrowResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.escrows[req.GetEscrowId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "escrow %s not found", req.GetEscrowId())
	}
	if rec.settled {
		return nil, status.Errorf(codes.FailedPrecondition, "escrow %s already settled", req.GetEscrowId())
	}
	return rec.info, nil
}

func (s *mockServer) GetParticipant(_ context.Context, req *mockchainpb.GetParticipantRequest) (*mockchainpb.GetParticipantResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.participants[req.GetAddress()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "participant %s not found", req.GetAddress())
	}
	return &mockchainpb.GetParticipantResponse{
		Address: p.address,
		Url:     p.url,
	}, nil
}

// GetGrantees returns every known participant address. In the testenv
// warm-key verification always succeeds: a host using any configured
// private key is a valid grantee.
func (s *mockServer) GetGrantees(_ context.Context, _ *mockchainpb.GetGranteesRequest) (*mockchainpb.GetGranteesResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	addrs := make([]string, 0, len(s.participants))
	for addr := range s.participants {
		addrs = append(addrs, addr)
	}
	return &mockchainpb.GetGranteesResponse{Addresses: addrs}, nil
}

// ── MockChain actions ─────────────────────────────────────────────────────────

func (s *mockServer) CreateEscrow(_ context.Context, req *mockchainpb.CreateEscrowRequest) (*mockchainpb.CreateEscrowResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	escrowID := req.GetEscrowId()
	if escrowID == "" {
		escrowID = s.cfg.Escrow.ID
	}
	if escrowID != s.cfg.Escrow.ID {
		return nil, status.Errorf(codes.InvalidArgument,
			"only the pre-configured escrow %s is supported, got %s",
			s.cfg.Escrow.ID, escrowID)
	}
	if _, ok := s.escrows[escrowID]; !ok {
		s.seedEscrow()
	}
	return &mockchainpb.CreateEscrowResponse{EscrowId: escrowID}, nil
}

func (s *mockServer) SettleEscrow(_ context.Context, req *mockchainpb.SettleEscrowRequest) (*mockchainpb.SettleEscrowResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.escrows[req.GetEscrowId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "escrow %s not found", req.GetEscrowId())
	}
	if rec.settled {
		return &mockchainpb.SettleEscrowResponse{Ok: true, Message: "already settled"}, nil
	}

	payload := state.SettlementPayload{
		EscrowID:   req.GetEscrowId(),
		Version:    req.GetVersion(),
		Nonce:      req.GetNonce(),
		Fees:       req.GetFees(),
		RestHash:   req.GetRestHash(),
		HostStats:  make(map[uint32]*types.HostStats, len(req.GetHostStats())),
		Signatures: make(map[uint32][]byte, len(req.GetSignatures())),
	}
	for _, hs := range req.GetHostStats() {
		payload.HostStats[hs.GetSlotId()] = &types.HostStats{
			Missed:               hs.GetMissed(),
			Invalid:              hs.GetInvalid(),
			Cost:                 hs.GetCost(),
			RequiredValidations:  hs.GetRequiredValidations(),
			CompletedValidations: hs.GetCompletedValidations(),
		}
	}
	for _, ss := range req.GetSignatures() {
		payload.Signatures[ss.GetSlotId()] = ss.GetSignature()
	}

	stateRoot, err := state.VerifySettlement(payload, rec.group, s.verifier, nil)
	if err != nil {
		return &mockchainpb.SettleEscrowResponse{
			Ok:      false,
			Message: fmt.Sprintf("settlement verification failed: %v", err),
		}, nil
	}

	rec.settled = true
	log.Printf("escrow %s settled at nonce %d version=%q state_root=%x",
		req.GetEscrowId(), req.GetNonce(), req.GetVersion(), stateRoot)
	return &mockchainpb.SettleEscrowResponse{
		Ok:        true,
		Message:   "settled",
		StateRoot: stateRoot,
	}, nil
}

// status is a read-only snapshot used by the HTTP /status endpoint.
func (s *mockServer) status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := fmt.Sprintf("escrows: %d\nparticipants: %d\n", len(s.escrows), len(s.participants))
	for id, rec := range s.escrows {
		out += fmt.Sprintf("  escrow %s settled=%v slots=%d version=%q\n",
			id, rec.settled, len(rec.info.Slots), rec.version)
	}
	return out
}
