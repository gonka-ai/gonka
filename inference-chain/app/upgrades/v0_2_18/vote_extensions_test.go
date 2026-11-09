package v0_2_18

import (
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

type memParamsStore struct {
	params cmtproto.ConsensusParams
	sets   int
	getErr error
	setErr error
}

func (m *memParamsStore) Get(context.Context) (cmtproto.ConsensusParams, error) {
	return m.params, m.getErr
}

func (m *memParamsStore) Set(_ context.Context, p cmtproto.ConsensusParams) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.sets++
	m.params = p
	return nil
}

func validParams(veHeight int64) cmtproto.ConsensusParams {
	p := cmtproto.ConsensusParams{
		Block:     &cmtproto.BlockParams{MaxBytes: 22020096, MaxGas: -1},
		Evidence:  &cmtproto.EvidenceParams{MaxAgeNumBlocks: 100000, MaxAgeDuration: 48 * time.Hour, MaxBytes: 1048576},
		Validator: &cmtproto.ValidatorParams{PubKeyTypes: []string{"ed25519"}},
		Version:   &cmtproto.VersionParams{App: 0},
	}
	if veHeight >= 0 {
		p.Abci = &cmtproto.ABCIParams{VoteExtensionsEnableHeight: veHeight}
	}
	return p
}

func ctxAt(height int64) context.Context {
	return sdk.NewContext(nil, cmtproto.Header{Height: height}, false, log.NewNopLogger())
}

func TestEnableVoteExtensions_Schedules(t *testing.T) {
	for name, ve := range map[string]int64{"abci zero": 0, "abci nil": -1} {
		t.Run(name, func(t *testing.T) {
			store := &memParamsStore{params: validParams(ve)}
			h, changed, err := EnableVoteExtensions(ctxAt(1000), store, VoteExtensionsEnableDelay)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, int64(1000)+VoteExtensionsEnableDelay, h)
			require.Equal(t, 1, store.sets)
			require.Equal(t, h, store.params.Abci.VoteExtensionsEnableHeight)
			// Other sections preserved.
			require.Equal(t, int64(22020096), store.params.Block.MaxBytes)
			require.Equal(t, []string{"ed25519"}, store.params.Validator.PubKeyTypes)
		})
	}
}

func TestEnableVoteExtensions_DoesNotMutateInput(t *testing.T) {
	orig := validParams(0)
	store := &memParamsStore{params: orig}
	_, _, err := EnableVoteExtensions(ctxAt(10), store, 5)
	require.NoError(t, err)
	require.Equal(t, int64(0), orig.Abci.VoteExtensionsEnableHeight)
}

func TestEnableVoteExtensions_AlreadyScheduledIsNoOp(t *testing.T) {
	for name, existing := range map[string]int64{"already enabled (past)": 2, "scheduled (future)": 5000} {
		t.Run(name, func(t *testing.T) {
			store := &memParamsStore{params: validParams(existing)}
			h, changed, err := EnableVoteExtensions(ctxAt(1000), store, VoteExtensionsEnableDelay)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, existing, h)
			require.Zero(t, store.sets)
		})
	}
}

func TestEnableVoteExtensions_RunTwiceIsIdempotent(t *testing.T) {
	store := &memParamsStore{params: validParams(0)}
	h1, changed, err := EnableVoteExtensions(ctxAt(50), store, 10)
	require.NoError(t, err)
	require.True(t, changed)

	h2, changed, err := EnableVoteExtensions(ctxAt(51), store, 10)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, h1, h2)
	require.Equal(t, 1, store.sets)
}

func TestEnableVoteExtensions_FailsCleanly(t *testing.T) {
	t.Run("delay < 1", func(t *testing.T) {
		store := &memParamsStore{params: validParams(0)}
		_, _, err := EnableVoteExtensions(ctxAt(10), store, 0)
		require.Error(t, err)
		require.Zero(t, store.sets)
	})
	t.Run("missing block section", func(t *testing.T) {
		p := validParams(0)
		p.Block = nil
		store := &memParamsStore{params: p}
		_, _, err := EnableVoteExtensions(ctxAt(10), store, 5)
		require.Error(t, err)
		require.Zero(t, store.sets)
	})
	t.Run("invalid params rejected before write", func(t *testing.T) {
		p := validParams(0)
		p.Block.MaxBytes = 0
		store := &memParamsStore{params: p}
		_, _, err := EnableVoteExtensions(ctxAt(10), store, 5)
		require.Error(t, err)
		require.Zero(t, store.sets)
	})
	t.Run("get error", func(t *testing.T) {
		store := &memParamsStore{params: validParams(0), getErr: errors.New("boom")}
		_, _, err := EnableVoteExtensions(ctxAt(10), store, 5)
		require.Error(t, err)
	})
	t.Run("set error", func(t *testing.T) {
		store := &memParamsStore{params: validParams(0), setErr: errors.New("boom")}
		_, _, err := EnableVoteExtensions(ctxAt(10), store, 5)
		require.Error(t, err)
	})
}

func TestEnableVoteExtensions_NilVersionTreatedAsZero(t *testing.T) {
	p := validParams(0)
	p.Version = nil
	store := &memParamsStore{params: p}
	_, changed, err := EnableVoteExtensions(ctxAt(10), store, 5)
	require.NoError(t, err)
	require.True(t, changed)
	require.Nil(t, store.params.Version, "must not add sections the chain did not have")
}
