package user

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"common/chainoracle/blocks"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestMainnetChatSizeRejectsBeforeJournalAndNonce(t *testing.T) {
	validator, owner := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "chat-size", InitialHeight: 100,
		Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}},
		Start:      time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := oracle.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{validator})
	const escrowID = "chat-size"
	const balance = 1_000_000
	st := storage.NewMemory()
	require.NoError(t, st.CreateSession(storage.CreateSessionParams{EscrowID: escrowID, Version: "v7",
		CreatorAddr: owner.Address(), Config: cfg, Group: group, InitialBalance: balance}))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(escrowID, cfg, group, balance, owner.Address(), verifier, st,
		state.WithVersion("v7"), state.WithMainnetHeights(oracle))
	require.NoError(t, err)
	session, err := NewSession(sm, owner, escrowID, group, []HostClient{nil}, verifier, WithStorage(st))
	require.NoError(t, err)
	defer session.Close()

	before, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	params := defaultParams
	params.Prompt = []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", int(transport.DefaultMaxBodySize)) + `"}]}`)
	_, err = session.PrepareInference(t.Context(), params)
	require.ErrorIs(t, err, transport.ErrChatRequestTooLarge)
	require.Zero(t, session.Nonce())
	require.Zero(t, sm.LatestNonce())
	diffs, err := st.GetDiffs(escrowID, 1, 1)
	require.NoError(t, err)
	require.Empty(t, diffs)
	after, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, before, after)
	small, err := session.PrepareInference(t.Context(), defaultParams)
	require.NoError(t, err)
	require.Equal(t, uint64(1), small.Nonce())
}

type sizedCatchUpClient struct {
	sent [][]uint64
}

func (c *sizedCatchUpClient) Send(_ context.Context, req host.HostRequest, _ io.Writer, _ func(*host.HostResponse)) (*host.HostResponse, error) {
	var chunk []uint64
	for _, diff := range req.Diffs {
		chunk = append(chunk, diff.Nonce)
	}
	c.sent = append(c.sent, chunk)
	return &host.HostResponse{Nonce: req.Nonce}, nil
}

func TestMainnetChatCatchUpSplitsByEncodedSize(t *testing.T) {
	owner, validator := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{validator})
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = 100, make([]byte, 32)
	const escrowID = "chat-size-catch-up"
	const balance = 1_000_000
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(escrowID, cfg, group, balance, owner.Address(), verifier,
		testutil.MustMemoryStore(t, escrowID, owner.Address(), cfg, group, balance), state.WithVersion("v7"))
	require.NoError(t, err)
	client := &sizedCatchUpClient{}
	session, err := NewSession(sm, owner, escrowID, group, []HostClient{client}, verifier)
	require.NoError(t, err)
	defer session.Close()

	// Stored wire fixtures isolate batching from the receiver's proof
	// verification. Each diff fits; the two together do not.
	for nonce := uint64(1); nonce <= 2; nonce++ {
		tx := &types.DevshardTx{Tx: &types.DevshardTx_MainnetHeight{MainnetHeight: &types.MsgMainnetHeight{
			Height: 100, LightBlocks: [][]byte{make([]byte, 4<<20)},
		}}}
		session.diffs = append(session.diffs, types.Diff{Nonce: nonce, Txs: []*types.DevshardTx{tx}})
		session.nonce = nonce
	}
	lastDiff := session.diffs[1]
	require.NoError(t, session.sendCatchUpThrough(t.Context(), 0, client, 2))
	require.Equal(t, [][]uint64{{1}, {2}}, client.sent)
	require.Equal(t, uint64(2), session.hostSyncNonce[0])

	session.hostSyncNonce[0] = 1
	session.diffs = []types.Diff{lastDiff}
	reason := types.TimeoutReason_TIMEOUT_REASON_REFUSED
	payload := &host.InferencePayload{Prompt: make([]byte, 8<<20)}
	tail, err := session.catchUpDiffsForVerifier(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, tail, 1)
	fits, err := transport.VerifyTimeoutFits(escrowID, 2, reason, payload, tail)
	require.NoError(t, err)
	require.False(t, fits)
	inline, err := session.catchUpForVote(t.Context(), 0, nil, func(diffs []types.Diff) (bool, error) {
		return transport.VerifyTimeoutFits(escrowID, 2, reason, payload, diffs)
	})
	require.NoError(t, err)
	require.Empty(t, inline, "the prompt fits after the verifier's signed diff was sent separately")
	require.Equal(t, [][]uint64{{1}, {2}, {2}}, client.sent)
}
