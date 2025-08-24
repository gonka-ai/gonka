package keeper_test

import (
	"encoding/base64"
	"encoding/hex"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"strings"
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
			SignedVotingPower:    80,
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
		assert.Equal(t, proof.SignedVotingPower, got.SignedVotingPower)

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

func TestName(t *testing.T) {
	base64ValAddr := "x0yMT+8gTLek0iUtbEJsQqPaqy0="
	hexValAddr := "C74C8C4FEF204CB7A4D2252D6C426C42A3DAAB2D"

	bytes, err := base64.StdEncoding.DecodeString(base64ValAddr)
	assert.NoError(t, err)
	hexStr := hex.EncodeToString(bytes)
	assert.Equal(t, strings.ToLower(hexStr), strings.ToLower(hexValAddr))

}
