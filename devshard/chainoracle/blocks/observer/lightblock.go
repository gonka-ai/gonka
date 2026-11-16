package observer

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	cmtsecp "github.com/cometbft/cometbft/crypto/secp256k1"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmversion "github.com/cometbft/cometbft/proto/tendermint/version"
	tmtypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

func (m *Mock) LightBlockAt(ctx context.Context, height int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.cfg.LightBlocks {
		return nil, blocks.ErrProveNotImplemented
	}
	raw := m.lightBlocks[height]
	if len(raw) == 0 {
		return nil, blocks.ErrHeaderNotFound
	}
	return bytes.Clone(raw), nil
}

func (m *Mock) fabricateLightBlock(height int64, t time.Time) (*blocks.Header, []byte, error) {
	validators := make([]*tmtypes.Validator, 0, len(m.cfg.Validators))
	for _, v := range m.cfg.Validators {
		validators = append(validators, tmtypes.NewValidator(cmtsecp.PubKey(v.Signer.CompressedPublicKeyBytes()), v.Power))
	}
	vals := tmtypes.NewValidatorSet(validators)
	var parent tmtypes.BlockID
	if m.latest != nil {
		previous, err := lightproof.Decode(m.lightBlocks[m.latest.Height])
		if err != nil {
			return nil, nil, err
		}
		parent = previous.Commit.BlockID
	}
	hdr := &tmtypes.Header{
		Version: tmversion.Consensus{Block: version.BlockProtocol}, ChainID: m.cfg.ChainID, Height: height, Time: t, LastBlockID: parent,
		LastCommitHash: m.deriveHash("commit", height), DataHash: m.deriveHash("data", height),
		ValidatorsHash: vals.Hash(), NextValidatorsHash: vals.Hash(), ConsensusHash: m.deriveHash("consensus", height),
		AppHash: m.deriveHash("app", height), LastResultsHash: m.deriveHash("results", height), EvidenceHash: m.deriveHash("evidence", height),
		ProposerAddress: vals.Validators[0].Address,
	}
	id := tmtypes.BlockID{Hash: hdr.Hash(), PartSetHeader: tmtypes.PartSetHeader{Total: 1, Hash: m.deriveHash("parts", height)}}
	sigs := make([]tmtypes.CommitSig, len(vals.Validators))
	for i, v := range vals.Validators {
		vote := &tmtypes.Vote{Type: tmproto.PrecommitType, Height: height, BlockID: id, Timestamp: t, ValidatorAddress: v.Address, ValidatorIndex: int32(i)}
		for _, sv := range m.cfg.Validators {
			pub := cmtsecp.PubKey(sv.Signer.CompressedPublicKeyBytes())
			if bytes.Equal(pub.Address(), v.Address) {
				sig, err := sv.Signer.Sign(tmtypes.VoteSignBytes(hdr.ChainID, vote.ToProto()))
				if err != nil {
					return nil, nil, err
				}
				if len(sig) != 65 {
					return nil, nil, fmt.Errorf("mock Comet signature length %d", len(sig))
				}
				vote.Signature = bytes.Clone(sig[:64])
				break
			}
		}
		sigs[i] = vote.CommitSig()
	}
	lb := &tmtypes.LightBlock{SignedHeader: &tmtypes.SignedHeader{Header: hdr, Commit: &tmtypes.Commit{Height: height, BlockID: id, Signatures: sigs}}, ValidatorSet: vals}
	pb, err := lb.ToProto()
	if err != nil {
		return nil, nil, err
	}
	raw, err := pb.Marshal()
	if err != nil {
		return nil, nil, err
	}
	// Verify the mock too: never let test fixtures bypass the production verifier.
	if _, err = lightproof.Decode(raw); err != nil {
		return nil, nil, err
	}
	out := &blocks.Header{Height: height, Time: t, ChainID: hdr.ChainID, BlockHash: bytes.Clone(hdr.Hash()), AppHash: hdr.AppHash, ValidatorsHash: hdr.ValidatorsHash, NextValidatorsHash: hdr.NextValidatorsHash, Commit: blocks.Commit{Height: height, BlockID: bytes.Clone(id.Hash)}}
	for _, sig := range sigs {
		out.Commit.Signatures = append(out.Commit.Signatures, blocks.CommitSig{ValidatorAddress: sig.ValidatorAddress, Timestamp: sig.Timestamp, Signature: sig.Signature})
	}
	return out, raw, nil
}
