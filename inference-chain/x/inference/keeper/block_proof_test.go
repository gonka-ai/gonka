package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestBlockProof(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	t.Run("get not existing block/pending proof", func(t *testing.T) {
		const height = 123
		_, found := k.GetBlockProof(ctx, height)
		assert.False(t, found)

		_, found = k.GetPendingProof(ctx, height)
		assert.False(t, found)
	})

	t.Run("set block proof", func(t *testing.T) {
		const height = 10
		proof := types.BlockProof{
			CreatedAtBlockHeight: height,
			AppHashHex:           "apphash-10",
			TotalVotingPower:     100,
		}

		_, found := k.GetBlockProof(ctx, height)
		assert.False(t, found)

		err := k.SetBlockProof(ctx, proof)
		assert.NoError(t, err)

		got, found := k.GetBlockProof(ctx, height)
		assert.True(t, found)
		assert.Equal(t, proof.CreatedAtBlockHeight, got.CreatedAtBlockHeight)
		assert.Equal(t, proof.AppHashHex, got.AppHashHex)
		assert.Equal(t, proof.TotalVotingPower, got.TotalVotingPower)

		err = k.SetBlockProof(ctx, proof)
		assert.Error(t, err, "duplicate SetBlockProof must fail")
	})

	t.Run("get block proof", func(t *testing.T) {
		h := int64(20)
		_, found := k.GetPendingProof(ctx, h)
		assert.False(t, found)

		epoch := uint64(345)
		k.SetPendingProof(ctx, h, epoch)

		pendingProofEpochId, found := k.GetPendingProof(ctx, h)
		assert.True(t, found)
		assert.Equal(t, epoch, pendingProofEpochId)

		k.SetPendingProof(ctx, h, 123214)

		pendingProofEpochId, found = k.GetPendingProof(ctx, h)
		assert.True(t, found)
		assert.Equal(t, epoch, pendingProofEpochId)
	})
}
