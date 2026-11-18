package lightproof

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmversion "github.com/cometbft/cometbft/proto/tendermint/version"
	tmtypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
	"github.com/stretchr/testify/require"
)

type proofChain struct {
	blocks map[int64][]byte
	reads  int
}

func (c *proofChain) LightBlockAt(_ context.Context, height int64) ([]byte, error) {
	c.reads++
	if raw := c.blocks[height]; raw != nil {
		return bytes.Clone(raw), nil
	}
	return nil, fmt.Errorf("missing block %d", height)
}

func signedBlock(t *testing.T, height int64, parent tmtypes.BlockID, vals, next *tmtypes.ValidatorSet, privs []tmtypes.PrivValidator) *tmtypes.LightBlock {
	t.Helper()
	h := &tmtypes.Header{
		Version: tmversion.Consensus{Block: version.BlockProtocol}, ChainID: "gonka-proof",
		Height: height, Time: time.Unix(1_700_000_000+height*5, 123).UTC(), LastBlockID: parent,
		LastCommitHash: tmhash.Sum([]byte("commit")), DataHash: tmhash.Sum([]byte("data")),
		ValidatorsHash: vals.Hash(), NextValidatorsHash: next.Hash(), ConsensusHash: tmhash.Sum([]byte("consensus")),
		AppHash: tmhash.Sum([]byte("state")), LastResultsHash: tmhash.Sum([]byte("results")), EvidenceHash: tmhash.Sum([]byte("evidence")),
		ProposerAddress: vals.Validators[0].Address,
	}
	id := tmtypes.BlockID{Hash: h.Hash(), PartSetHeader: tmtypes.PartSetHeader{Total: 1, Hash: tmhash.Sum([]byte("parts"))}}
	sigs := make([]tmtypes.CommitSig, len(vals.Validators))
	for i, val := range vals.Validators {
		vote := &tmtypes.Vote{Type: tmproto.PrecommitType, Height: height, BlockID: id, Timestamp: h.Time, ValidatorAddress: val.Address, ValidatorIndex: int32(i)}
		pb := vote.ToProto()
		for _, priv := range privs {
			pub, err := priv.GetPubKey()
			require.NoError(t, err)
			if bytes.Equal(pub.Address(), val.Address) {
				require.NoError(t, priv.SignVote(h.ChainID, pb))
				break
			}
		}
		vote.Signature = pb.Signature
		sigs[i] = vote.CommitSig()
	}
	return &tmtypes.LightBlock{SignedHeader: &tmtypes.SignedHeader{Header: h, Commit: &tmtypes.Commit{Height: height, BlockID: id, Signatures: sigs}}, ValidatorSet: vals}
}

func encodeBlock(t *testing.T, lb *tmtypes.LightBlock) []byte {
	t.Helper()
	pb, err := lb.ToProto()
	require.NoError(t, err)
	raw, err := pb.Marshal()
	require.NoError(t, err)
	return raw
}

func proofFixture(t *testing.T) (*proofChain, Checkpoint) {
	t.Helper()
	a, privA := tmtypes.RandValidatorSet(4, 10)
	b, privB := tmtypes.RandValidatorSet(4, 10)
	c := &proofChain{blocks: make(map[int64][]byte)}
	var parent tmtypes.BlockID
	var anchor Checkpoint
	for h := int64(1); h <= 10; h++ {
		vals, next, privs := a, a, privA
		if h == 4 {
			next = b
		}
		if h >= 5 {
			vals, next, privs = b, b, privB
		}
		lb := signedBlock(t, h, parent, vals, next, privs)
		c.blocks[h] = encodeBlock(t, lb)
		parent = lb.Commit.BlockID
		if h == 1 {
			anchor = Checkpoint{Height: h, Hash: bytes.Clone(lb.Hash())}
		}
	}
	return c, anchor
}

func TestProofBindsBootstrapAndValidatorTransitions(t *testing.T) {
	src, anchor := proofFixture(t)
	proof, cp, err := Build(context.Background(), anchor, 10, src)
	require.NoError(t, err)
	require.Less(t, len(proof), 10, "unchanged validator sets need not carry every block")
	got, err := Verify(anchor, 10, proof)
	require.NoError(t, err)
	require.Equal(t, cp, got)
	require.Equal(t, int64(10), got.Height)
	require.Equal(t, "gonka-proof", got.ChainID)
	// Replay long after the original block is independent of wall-clock time.
	replay, err := Verify(anchor, 10, proof)
	require.NoError(t, err)
	require.Equal(t, got, replay)
	reads := src.reads
	empty, same, err := Build(context.Background(), got, 10, src)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Equal(t, got, same)
	require.Equal(t, reads, src.reads)
}

func TestProofRejectsSelfSignedReplacementSet(t *testing.T) {
	src, anchor := proofFixture(t)
	bootstrap, err := Decode(src.blocks[1])
	require.NoError(t, err)
	cp := checkpoint(bootstrap)
	// Block 5 has a valid commit, but its entirely different validator set
	// becomes trusted only through block 4's authenticated NextValidatorsHash.
	_, err = Verify(cp, 5, [][]byte{src.blocks[5]})
	require.ErrorIs(t, err, ErrInvalid)
	wrong := Checkpoint{Height: 1, Hash: tmhash.Sum([]byte("another chain"))}
	_, err = Verify(wrong, 1, [][]byte{src.blocks[1]})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Verify(anchor, 1, nil)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestProofRejectsForgedCommitAndUnlinkedHeaders(t *testing.T) {
	src, anchor := proofFixture(t)
	for _, mutate := range []func(*tmtypes.LightBlock){
		func(lb *tmtypes.LightBlock) {
			lb.Commit.Signatures[0].Signature[0] ^= 1
			lb.Commit.Signatures[1].Signature[0] ^= 1
		},
		func(lb *tmtypes.LightBlock) { lb.Header.Time = lb.Header.Time.Add(time.Hour) },
		func(lb *tmtypes.LightBlock) { lb.ValidatorSet.Validators[0].VotingPower++ },
	} {
		lb, err := Decode(src.blocks[1])
		require.NoError(t, err)
		mutate(lb)
		_, err = Verify(anchor, 1, [][]byte{encodeBlock(t, lb)})
		require.ErrorIs(t, err, ErrInvalid)
	}
	base, err := Decode(src.blocks[1])
	require.NoError(t, err)
	vals, privs := tmtypes.RandValidatorSet(4, 10)
	other := signedBlock(t, 2, tmtypes.BlockID{Hash: tmhash.Sum([]byte("fork")), PartSetHeader: base.Commit.BlockID.PartSetHeader}, vals, vals, privs)
	_, err = Verify(checkpoint(base), 2, [][]byte{encodeBlock(t, other)})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Verify(checkpoint(base), 0, nil)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Decode(make([]byte, MaxBlockBytes+1))
	require.ErrorIs(t, err, ErrInvalid)
}

func TestProofRejectsMissingTargetAndReordering(t *testing.T) {
	src, anchor := proofFixture(t)
	proof, cp, err := Build(context.Background(), anchor, 10, src)
	require.NoError(t, err)
	_, err = Verify(anchor, 11, proof)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Verify(cp, 9, nil)
	require.ErrorIs(t, err, ErrInvalid)
	proof[0], proof[len(proof)-1] = proof[len(proof)-1], proof[0]
	_, err = Verify(anchor, 10, proof)
	require.ErrorIs(t, err, ErrInvalid)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = Build(ctx, cp, 11, src)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDecodeRejectsMissingLightBlockComponentsWithoutPanicking(t *testing.T) {
	src, _ := proofFixture(t)
	for name, mutate := range map[string]func(*tmproto.LightBlock){
		"signed header": func(pb *tmproto.LightBlock) { pb.SignedHeader = nil },
		"header":        func(pb *tmproto.LightBlock) { pb.SignedHeader.Header = nil },
		"commit":        func(pb *tmproto.LightBlock) { pb.SignedHeader.Commit = nil },
		"validators":    func(pb *tmproto.LightBlock) { pb.ValidatorSet = nil },
	} {
		t.Run(name, func(t *testing.T) {
			var pb tmproto.LightBlock
			require.NoError(t, pb.Unmarshal(src.blocks[1]))
			mutate(&pb)
			raw, err := pb.Marshal()
			require.NoError(t, err)
			require.NotPanics(t, func() {
				_, err := Decode(raw)
				require.ErrorIs(t, err, ErrInvalid)
			})
		})
	}
	require.NotPanics(t, func() {
		_, err := Decode([]byte{0x0a, 0x00})
		require.ErrorIs(t, err, ErrInvalid)
	})
}
