package utils

import (
	cryptotypes "github.com/cometbft/cometbft/proto/tendermint/crypto"
	"github.com/gonka-ai/gonka-utils/go/contracts"
	"github.com/productscience/inference/x/inference/types"
)

func ToCryptoProofOps(src *types.ProofOps) *cryptotypes.ProofOps {
	if src == nil {
		return nil
	}

	proofOps := &cryptotypes.ProofOps{
		Ops: make([]cryptotypes.ProofOp, 0),
	}
	for _, op := range src.Ops {
		proofOps.Ops = append(proofOps.Ops, cryptotypes.ProofOp(op))

	}
	return proofOps
}

func ToContractsValidatorsProof(src *types.ValidatorsProof) *contracts.ValidatorsProof {
	if src == nil {
		return nil
	}
	out := &contracts.ValidatorsProof{
		BlockHeight: src.BlockHeight,
		Round:       src.Round,
		BlockId: &contracts.BlockID{
			Hash:               src.BlockId.Hash,
			PartSetHeaderTotal: src.BlockId.PartSetHeaderTotal,
			PartSetHeaderHash:  src.BlockId.PartSetHeaderHash,
		},
		Signatures: make([]*contracts.SignatureInfo, len(src.Signatures)),
	}
	for i, s := range src.Signatures {
		out.Signatures[i] = &contracts.SignatureInfo{
			SignatureBase64:     s.SignatureBase64,
			ValidatorAddressHex: s.ValidatorAddressHex,
			Timestamp:           s.Timestamp,
		}
	}
	return out
}

func ToContractsBlockProof(src *types.BlockProof) *contracts.BlockProof {
	if src == nil {
		return nil
	}
	out := &contracts.BlockProof{
		CreatedAtBlockHeight: src.CreatedAtBlockHeight,
		AppHashHex:           src.AppHashHex,
		TotalVotingPower:     src.TotalVotingPower,
		Commits:              make([]*contracts.CommitInfo, len(src.Commits)),
	}
	for i, c := range src.Commits {
		out.Commits[i] = &contracts.CommitInfo{
			ValidatorAddress: c.ValidatorAddress,
			ValidatorPubKey:  c.ValidatorPubKey,
			VotingPower:      c.VotingPower,
		}
	}
	return out
}
