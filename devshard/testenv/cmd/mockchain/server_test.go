package main

import (
	"context"
	"net"
	"sort"
	"strconv"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"devshard/signing"
	"devshard/state"
	"devshard/testenv/config"
	"devshard/testenv/proto/mockchainpb"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

const bufSize = 1 << 16

// ── Test fixtures ─────────────────────────────────────────────────────────────

type fixture struct {
	cfg     *config.Config
	signers []*signing.Secp256k1Signer
	client  mockchainpb.MockChainClient
	cleanup func()
}

func newFixture(t *testing.T, numHosts int) *fixture {
	t.Helper()

	signers := make([]*signing.Secp256k1Signer, numHosts)
	hosts := make([]config.HostCfg, numHosts)
	for i := range signers {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		signers[i] = s
		hosts[i] = config.HostCfg{
			ID:      "devshardd-testenv-" + strconv.Itoa(i),
			Address: s.Address(),
			SlotIDs: []int{i},
			Port:    8080 + i,
		}
	}

	userKey, err := signing.GenerateKey()
	require.NoError(t, err)

	cfg := &config.Config{
		Chain:    config.ChainCfg{ID: "gonka-testenv-1"},
		Escrow:   config.EscrowCfg{Slots: numHosts},
		Hosts:    hosts,
		User:     config.UserCfg{Address: userKey.Address()},
		Engine:   config.EngineCfg{},
		Network:  config.NetworkCfg{},
	}
	// Apply defaults via a round-trip through applyDefaults by loading+saving.
	// Simpler: manually apply the defaults we care about.
	cfg.Chain.BlockTime = "1s"
	cfg.MockChain.Port = 9090
	cfg.Escrow.ID = "1"
	cfg.Escrow.Version = "v1"
	cfg.Escrow.Amount = 1_000_000
	cfg.Escrow.TokenPrice = 1
	cfg.Escrow.AppHash = config.DefaultAppHash
	cfg.Devshard.GroupSize = numHosts
	for i := range cfg.Hosts {
		cfg.Hosts[i].URL = cfg.Hosts[i].HostURL()
	}
	require.NoError(t, cfg.Validate())

	srv := newMockServer(cfg)

	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer()
	mockchainpb.RegisterMockChainServer(gs, srv)

	serveDone := make(chan struct{})
	go func() {
		_ = gs.Serve(lis)
		close(serveDone)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	return &fixture{
		cfg:     cfg,
		signers: signers,
		client:  mockchainpb.NewMockChainClient(conn),
		cleanup: func() {
			_ = conn.Close()
			gs.GracefulStop()
			<-serveDone
		},
	}
}

// ── Queries ───────────────────────────────────────────────────────────────────

func TestMockServer_GetDevshardEscrow_ReturnsSeeded(t *testing.T) {
	f := newFixture(t, 3)
	defer f.cleanup()

	resp, err := f.client.GetDevshardEscrow(context.Background(),
		&mockchainpb.GetDevshardEscrowRequest{EscrowId: "1"},
	)
	require.NoError(t, err)
	require.Equal(t, "1", resp.EscrowId)
	require.Equal(t, uint64(1_000_000), resp.Amount)
	require.Equal(t, uint64(1), resp.TokenPrice)
	require.Len(t, resp.Slots, 3)
	require.Equal(t, f.signers[0].Address(), resp.Slots[0])
	require.Equal(t, f.signers[1].Address(), resp.Slots[1])
	require.Equal(t, f.signers[2].Address(), resp.Slots[2])
}

func TestMockServer_GetDevshardEscrow_NotFound(t *testing.T) {
	f := newFixture(t, 1)
	defer f.cleanup()

	_, err := f.client.GetDevshardEscrow(context.Background(),
		&mockchainpb.GetDevshardEscrowRequest{EscrowId: "missing"},
	)
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestMockServer_GetParticipant_ReturnsURL(t *testing.T) {
	f := newFixture(t, 2)
	defer f.cleanup()

	resp, err := f.client.GetParticipant(context.Background(),
		&mockchainpb.GetParticipantRequest{Address: f.signers[1].Address()},
	)
	require.NoError(t, err)
	require.Equal(t, f.signers[1].Address(), resp.Address)
	require.Equal(t, f.cfg.Hosts[1].HostURL(), resp.Url)

	_, err = f.client.GetParticipant(context.Background(),
		&mockchainpb.GetParticipantRequest{Address: "gonka1unknown"},
	)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestMockServer_GetGrantees_ReturnsAllParticipants(t *testing.T) {
	f := newFixture(t, 3)
	defer f.cleanup()

	resp, err := f.client.GetGrantees(context.Background(),
		&mockchainpb.GetGranteesRequest{ValidatorAddress: "any"},
	)
	require.NoError(t, err)

	got := append([]string(nil), resp.Addresses...)
	want := []string{
		f.signers[0].Address(),
		f.signers[1].Address(),
		f.signers[2].Address(),
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}

// ── Actions ───────────────────────────────────────────────────────────────────

func TestMockServer_CreateEscrow_Idempotent(t *testing.T) {
	f := newFixture(t, 1)
	defer f.cleanup()

	r1, err := f.client.CreateEscrow(context.Background(), &mockchainpb.CreateEscrowRequest{})
	require.NoError(t, err)
	require.Equal(t, "1", r1.EscrowId)

	r2, err := f.client.CreateEscrow(context.Background(), &mockchainpb.CreateEscrowRequest{EscrowId: "1"})
	require.NoError(t, err)
	require.Equal(t, "1", r2.EscrowId)

	_, err = f.client.CreateEscrow(context.Background(), &mockchainpb.CreateEscrowRequest{EscrowId: "42"})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestMockServer_SettleEscrow_AcceptsValidQuorum(t *testing.T) {
	f := newFixture(t, 3)
	defer f.cleanup()

	req := f.buildSettlement(t)
	resp, err := f.client.SettleEscrow(context.Background(), req)
	require.NoError(t, err)
	require.True(t, resp.Ok, "rejected: %s", resp.Message)
	require.NotEmpty(t, resp.StateRoot)

	// Second call returns already-settled success.
	again, err := f.client.SettleEscrow(context.Background(), req)
	require.NoError(t, err)
	require.True(t, again.Ok)
	require.Contains(t, again.Message, "already")

	// GetDevshardEscrow now refuses: FailedPrecondition.
	_, err = f.client.GetDevshardEscrow(context.Background(),
		&mockchainpb.GetDevshardEscrowRequest{EscrowId: "1"},
	)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestMockServer_SettleEscrow_RejectsBadQuorum(t *testing.T) {
	f := newFixture(t, 3)
	defer f.cleanup()

	req := f.buildSettlement(t)
	// Drop two signatures; keep only one (below 2/3+1 quorum).
	req.Signatures = req.Signatures[:1]

	resp, err := f.client.SettleEscrow(context.Background(), req)
	require.NoError(t, err)
	require.False(t, resp.Ok)
	require.Contains(t, resp.Message, "insufficient quorum")
}

func TestMockServer_SettleEscrow_RejectsTamperedSignature(t *testing.T) {
	f := newFixture(t, 3)
	defer f.cleanup()

	req := f.buildSettlement(t)
	// Flip a bit in the first signature.
	req.Signatures[0].Signature[0] ^= 0xFF

	resp, err := f.client.SettleEscrow(context.Background(), req)
	require.NoError(t, err)
	require.False(t, resp.Ok)
	require.NotEmpty(t, resp.Message)
}

func TestMockServer_SettleEscrow_RejectsEmptyVersion(t *testing.T) {
	f := newFixture(t, 3)
	defer f.cleanup()

	req := f.buildSettlement(t)
	req.Version = ""

	resp, err := f.client.SettleEscrow(context.Background(), req)
	require.NoError(t, err)
	require.False(t, resp.Ok)
	require.Contains(t, resp.Message, "empty version")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// buildSettlement constructs a valid SettleEscrowRequest signed by every
// host in the fixture. Keeping the helper co-located with the tests (vs.
// a shared file) makes the reason each field is present obvious: every
// line maps to one piece of what state.VerifySettlement checks.
func (f *fixture) buildSettlement(t *testing.T) *mockchainpb.SettleEscrowRequest {
	t.Helper()

	hostStats := map[uint32]*types.HostStats{}
	for i := range f.signers {
		hostStats[uint32(i)] = &types.HostStats{Cost: uint64(100 * (i + 1))}
	}

	st := types.EscrowState{
		Balance:   9900,
		Version:   f.cfg.Escrow.Version,
		HostStats: hostStats,
		Inferences: map[uint64]*types.InferenceRecord{
			1: {Status: types.StatusFinished, ExecutorSlot: 0, ActualCost: 100},
		},
	}

	payload, err := state.BuildSettlement(f.cfg.Escrow.ID, st, nil, 5)
	require.NoError(t, err)

	hostStatsHash, err := state.ComputeHostStatsHash(hostStats)
	require.NoError(t, err)
	stateRoot := state.ComputeStateRootFromRestHash(
		hostStatsHash, payload.RestHash, payload.Fees,
		types.PhaseSettlement, payload.Version,
	)

	sigContent := &types.StateSignatureContent{
		StateRoot: stateRoot,
		EscrowId:  f.cfg.Escrow.ID,
		Nonce:     payload.Nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)

	req := &mockchainpb.SettleEscrowRequest{
		EscrowId: f.cfg.Escrow.ID,
		Version:  payload.Version,
		Nonce:    payload.Nonce,
		Fees:     payload.Fees,
		RestHash: payload.RestHash,
	}
	for slot, hs := range payload.HostStats {
		req.HostStats = append(req.HostStats, &mockchainpb.HostStats{
			SlotId:               slot,
			Missed:               hs.Missed,
			Invalid:              hs.Invalid,
			Cost:                 hs.Cost,
			RequiredValidations:  hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		})
	}
	for i, s := range f.signers {
		sig, err := s.Sign(sigData)
		require.NoError(t, err)
		req.Signatures = append(req.Signatures, &mockchainpb.SlotSignature{
			SlotId:    uint32(i),
			Signature: sig,
		})
	}
	// Sort host_stats by slot_id for stable test output.
	sort.Slice(req.HostStats, func(i, j int) bool { return req.HostStats[i].SlotId < req.HostStats[j].SlotId })
	sort.Slice(req.Signatures, func(i, j int) bool { return req.Signatures[i].SlotId < req.Signatures[j].SlotId })
	return req
}
