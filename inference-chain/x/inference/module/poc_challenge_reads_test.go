package inference

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/store/tracekv"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/stretchr/testify/require"
)

// Evaluating a challenge segment reads its target participant once.
func TestDecideCurrentChallengeSegmentReadsTargetOnce(t *testing.T) {
	am, k, ctx := prepareChallengeEval(t, 400, 0, []challengeEvalModel{
		{id: "m1", weight: 100, count: 80, accept: true},
	})
	var trace bytes.Buffer
	traced := ctx.WithMultiStore(tracedMultiStore{ctx.MultiStore(), &trace})
	require.NoError(t, am.decideCurrentChallengeSegments(traced, 2, 500, 180, false))

	p, ok := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, ok)
	require.NotNil(t, p.CurrentEpochStats.ConfirmationPoCRatio, "the segment was evaluated")

	m := k.Participants
	key, err := collections.EncodeKeyWithPrefix(m.GetPrefix(), m.KeyCodec(), sdk.MustAccAddressFromBech32(testutil.Executor))
	require.NoError(t, err)
	enc := base64.StdEncoding.EncodeToString(key)
	reads := 0
	for _, line := range strings.Split(trace.String(), "\n") {
		if strings.Contains(line, `"operation":"read"`) && strings.Contains(line, `"key":"`+enc+`"`) {
			reads++
		}
	}
	require.Equal(t, 1, reads)
}

// tracedMultiStore traces every KVStore access, including reads a cache layer
// below would answer and those made through a CacheContext.
type tracedMultiStore struct {
	storetypes.MultiStore
	w *bytes.Buffer
}

func (m tracedMultiStore) GetKVStore(key storetypes.StoreKey) storetypes.KVStore {
	return tracekv.NewStore(m.MultiStore.GetKVStore(key), m.w, nil)
}

func (m tracedMultiStore) CacheMultiStore() storetypes.CacheMultiStore {
	return tracedCacheMultiStore{m.MultiStore.CacheMultiStore(), m.w}
}

type innerCacheMultiStore = storetypes.CacheMultiStore

type tracedCacheMultiStore struct {
	innerCacheMultiStore
	w *bytes.Buffer
}

func (m tracedCacheMultiStore) GetKVStore(key storetypes.StoreKey) storetypes.KVStore {
	return tracekv.NewStore(m.innerCacheMultiStore.GetKVStore(key), m.w, nil)
}

func (m tracedCacheMultiStore) CacheMultiStore() storetypes.CacheMultiStore {
	return tracedCacheMultiStore{m.innerCacheMultiStore.CacheMultiStore(), m.w}
}
