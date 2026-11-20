package user

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestSettlementCurrentGrantsAreReadConcurrently(t *testing.T) {
	colds := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(colds)
	owner := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(colds[0].CompressedPublicKeyBytes())
	require.NoError(t, err)
	oracle, err := mockoracle.NewMock(mockoracle.MockConfig{
		LightBlocks: true, ChainID: "settlement-parallel", InitialHeight: 10,
		Validators: []mockoracle.MockValidator{{Signer: colds[0], Address: addr, Power: 10}},
		Start:      time.Now().Add(-time.Minute), BlockInterval: time.Second,
	})
	require.NoError(t, err)
	anchor, err := oracle.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(len(group))
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	const escrowID, balance = "parallel-grant-check", 1_000_000
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine(escrowID, cfg, group, balance, owner.Address(), verifier,
		testutil.MustMemoryStore(t, escrowID, owner.Address(), cfg, group, balance),
		state.WithVersion("v7"), state.WithMainnetHeights(oracle),
		state.WithWarmKeyHeightChecker(func(string, string, uint64, int64, time.Time) (bool, error) { return true, nil }))
	require.NoError(t, err)
	proof, err := sm.BuildMainnetHeightTx(t.Context())
	require.NoError(t, err)
	txs := []*types.DevshardTx{proof}
	warm := make(map[string]string)
	for _, slot := range group {
		key := testutil.MustGenerateKey(t).Address()
		warm[slot.ValidatorAddress] = key
		txs = append(txs, testutil.WarmKeyBindTx(slot.SlotID, key, uint64(slot.SlotID)+1))
	}
	_, err = sm.ApplyLocal(1, txs)
	require.NoError(t, err)
	s := &Session{sm: sm, group: group}
	q := &SettlementQuorum{session: s, nonce: 1, candidates: make(map[uint32]map[string][]byte)}
	for _, slot := range group {
		q.addCandidate(slot.SlotID, warm[slot.ValidatorAddress], []byte{byte(slot.SlotID)})
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var started atomic.Int32
	allStarted := make(chan struct{})
	selected, missing := q.selectSignatures(ctx, func(ctx context.Context, cold string) (map[string]struct{}, error) {
		if started.Add(1) == int32(len(group)) {
			close(allStarted)
		}
		select {
		case <-allStarted:
			return map[string]struct{}{warm[cold]: {}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, nil)
	require.Equal(t, int32(len(group)), started.Load())
	require.Len(t, selected, len(group))
	require.Empty(t, missing)
}
