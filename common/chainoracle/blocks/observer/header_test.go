package observer

import (
	"bytes"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestNewTendermint_EmptyRPCURL(t *testing.T) {
	_, err := NewTendermint(TendermintConfig{})
	require.Error(t, err)
}

func TestObserver_ResultBlockToHeader_HashOnly(t *testing.T) {
	hdr := cmttypes.Header{
		Version: cmtversion.Consensus{Block: 11},
		ChainID: "gonka-test",
		Height:  42,
		Time:    time.Unix(1_700_000_000, 0).UTC(),
		LastBlockID: cmttypes.BlockID{
			Hash: bytes.Repeat([]byte{1}, tmhash.Size),
			PartSetHeader: cmttypes.PartSetHeader{
				Total: 1,
				Hash:  bytes.Repeat([]byte{2}, tmhash.Size),
			},
		},
		LastCommitHash:     bytes.Repeat([]byte{3}, tmhash.Size),
		DataHash:           bytes.Repeat([]byte{4}, tmhash.Size),
		ValidatorsHash:     bytes.Repeat([]byte{5}, tmhash.Size),
		NextValidatorsHash: bytes.Repeat([]byte{6}, tmhash.Size),
		ConsensusHash:      bytes.Repeat([]byte{7}, tmhash.Size),
		AppHash:            []byte{9, 9, 9},
		LastResultsHash:    bytes.Repeat([]byte{8}, tmhash.Size),
		EvidenceHash:       bytes.Repeat([]byte{10}, tmhash.Size),
		ProposerAddress:    bytes.Repeat([]byte{11}, 20),
	}
	block := &cmttypes.Block{Header: hdr}
	res := &ctypes.ResultBlock{
		Block:   block,
		BlockID: cmttypes.BlockID{Hash: block.Hash()},
	}

	got, err := HeaderFromResultBlock(res)
	require.NoError(t, err)
	require.Equal(t, int64(42), got.Height)
	require.Equal(t, "gonka-test", got.ChainID)
	require.Equal(t, block.Hash().Bytes(), got.BlockHash)
	require.Empty(t, got.Commit.Signatures, "hash-only header; Commit stays empty until Strong")
	require.Empty(t, got.ValidatorsHash)
	require.Empty(t, got.NextValidatorsHash)
	require.Empty(t, got.AppHash)
}

func TestHeaderFromNewBlock(t *testing.T) {
	block := cmttypes.MakeBlock(12, nil, nil, nil)
	block.Header.ChainID = "gonka-test"
	block.Header.Time = time.Unix(1_700_000_000, 0).UTC()
	block.Header.ValidatorsHash = bytes.Repeat([]byte{0xab}, 32)
	want := block.Header.Hash().Bytes()
	got, ok := HeaderFromNewBlock(cmttypes.EventDataNewBlock{
		Block:   block,
		BlockID: cmttypes.BlockID{Hash: want},
	})
	require.True(t, ok)
	require.Equal(t, int64(12), got.Height)
	require.Equal(t, want, got.BlockHash)
	require.Equal(t, "gonka-test", got.ChainID)
}

func TestHeaderFromResultHeader_MatchesBlockHash(t *testing.T) {
	block := cmttypes.MakeBlock(12, nil, nil, nil)
	block.Header.ChainID = "gonka-test"
	block.Header.Time = time.Unix(1_700_000_000, 0).UTC()
	block.Header.ValidatorsHash = bytes.Repeat([]byte{0xab}, 32)
	want := block.Header.Hash()
	fromBlock, err := HeaderFromResultBlock(&ctypes.ResultBlock{
		Block:   block,
		BlockID: cmttypes.BlockID{Hash: want},
	})
	require.NoError(t, err)
	fromHeader, err := HeaderFromResultHeader(&ctypes.ResultHeader{Header: &block.Header})
	require.NoError(t, err)
	require.Equal(t, want.Bytes(), fromBlock.BlockHash)
	require.Equal(t, want.Bytes(), fromHeader.BlockHash)
	require.Equal(t, fromBlock.Height, fromHeader.Height)
}

func TestHeaderFromResultHeader_NilIsNotFound(t *testing.T) {
	_, err := HeaderFromResultHeader(&ctypes.ResultHeader{})
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
}
