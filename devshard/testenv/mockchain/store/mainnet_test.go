package store

import (
	"common/chainoracle/blocks/lightproof"
	"context"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestMainnetStandUsesOnePinnedSignedChain(t *testing.T) {
	s := New()
	s.MainnetProofs = true
	s.ChainID = "gonka-stand"
	s.BlockHeight = 150
	s.Escrows[1] = &inferencetypes.DevshardEscrow{Id: 1}
	require.NoError(t, s.EnableMainnetProofs(time.Second))
	anchor := s.GetEscrow(1)
	require.NotNil(t, anchor)
	require.Equal(t, int64(150), anchor.MainnetAnchorHeight)
	require.Len(t, anchor.MainnetAnchorHash, 32)
	s.AdvanceBlock()
	s.AdvanceBlock()
	again := s.GetEscrow(1)
	require.Equal(t, anchor.MainnetAnchorHeight, again.MainnetAnchorHeight, "a later reader must see the creation anchor")
	require.Equal(t, anchor.MainnetAnchorHash, again.MainnetAnchorHash)
	again.MainnetAnchorHash[0] ^= 1
	require.Equal(t, anchor.MainnetAnchorHash, s.GetEscrow(1).MainnetAnchorHash, "query responses must not alias stored anchors")
	source := s.MainnetOracle()
	evidence, ok := source.(lightproof.Source)
	require.True(t, ok)
	proof, cp, err := lightproof.Build(context.Background(), lightproof.Checkpoint{Height: anchor.MainnetAnchorHeight, Hash: anchor.MainnetAnchorHash}, 152, evidence)
	require.NoError(t, err)
	verified, err := lightproof.Verify(lightproof.Checkpoint{Height: anchor.MainnetAnchorHeight, Hash: anchor.MainnetAnchorHash}, 152, proof)
	require.NoError(t, err)
	require.Equal(t, cp, verified)
	h, err := source.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, cp.Hash, h.BlockHash)
	s.PutEscrow(&inferencetypes.DevshardEscrow{Id: 2})
	next := s.GetEscrow(2)
	require.NotNil(t, next)
	require.Equal(t, int64(152), next.MainnetAnchorHeight)
	require.Equal(t, cp.Hash, next.MainnetAnchorHash)
}
