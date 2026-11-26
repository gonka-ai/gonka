package observer

import (
	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLightBlockEvidenceNeedsTheConsumersPinnedAnchor(t *testing.T) {
	chain := newSignedChain(t, 10)
	o := newOracle(chain) // evidence is also available in hash-only mode
	raw, err := o.LightBlockAt(context.Background(), 3)
	require.NoError(t, err)
	lb, err := lightproof.Decode(raw)
	require.NoError(t, err)
	require.Equal(t, int64(3), lb.Height)
	anchor := lightproof.Checkpoint{Height: 3, Hash: chain.headers[3].Hash()}
	proof, expected, err := lightproof.Build(context.Background(), anchor, 10, o)
	require.NoError(t, err)
	verified, err := lightproof.Verify(anchor, 10, proof)
	require.NoError(t, err)
	require.Equal(t, expected, verified)
	anchor.Hash = make([]byte, 32)
	_, err = lightproof.Verify(anchor, 10, proof)
	require.ErrorIs(t, err, lightproof.ErrInvalid)
	_, err = o.LightBlockAt(context.Background(), 0)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
}
