package nmrpc

import (
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"
)

// HeaderToProto maps a BlockOracle header onto the NodeManager wire type.
func HeaderToProto(h *blocks.Header) *gen.BlockHeader {
	if h == nil {
		return nil
	}
	out := &gen.BlockHeader{
		Height:             h.Height,
		TimeUnixNano:       h.Time.UTC().UnixNano(),
		ChainId:            h.ChainID,
		BlockHash:          append([]byte(nil), h.BlockHash...),
		AppHash:            append([]byte(nil), h.AppHash...),
		ValidatorsHash:     append([]byte(nil), h.ValidatorsHash...),
		NextValidatorsHash: append([]byte(nil), h.NextValidatorsHash...),
		Commit: &gen.BlockCommit{
			Height:  h.Commit.Height,
			Round:   h.Commit.Round,
			BlockId: append([]byte(nil), h.Commit.BlockID...),
		},
	}
	if h.Time.IsZero() {
		out.TimeUnixNano = 0
	}
	if len(h.Commit.Signatures) == 0 {
		return out
	}
	out.Commit.Signatures = make([]*gen.BlockCommitSig, len(h.Commit.Signatures))
	for i, s := range h.Commit.Signatures {
		ts := int64(0)
		if !s.Timestamp.IsZero() {
			ts = s.Timestamp.UTC().UnixNano()
		}
		out.Commit.Signatures[i] = &gen.BlockCommitSig{
			ValidatorAddress:  append([]byte(nil), s.ValidatorAddress...),
			TimestampUnixNano: ts,
			Signature:         append([]byte(nil), s.Signature...),
		}
	}
	return out
}

// HeaderFromProto is the inverse of HeaderToProto.
func HeaderFromProto(p *gen.BlockHeader) *blocks.Header {
	if p == nil {
		return nil
	}
	h := &blocks.Header{
		Height:             p.GetHeight(),
		ChainID:            p.GetChainId(),
		BlockHash:          append([]byte(nil), p.GetBlockHash()...),
		AppHash:            append([]byte(nil), p.GetAppHash()...),
		ValidatorsHash:     append([]byte(nil), p.GetValidatorsHash()...),
		NextValidatorsHash: append([]byte(nil), p.GetNextValidatorsHash()...),
	}
	if p.GetTimeUnixNano() != 0 {
		h.Time = time.Unix(0, p.GetTimeUnixNano()).UTC()
	}
	c := p.GetCommit()
	if c == nil {
		return h
	}
	h.Commit = blocks.Commit{
		Height:  c.GetHeight(),
		Round:   c.GetRound(),
		BlockID: append([]byte(nil), c.GetBlockId()...),
	}
	sigs := c.GetSignatures()
	if len(sigs) == 0 {
		return h
	}
	h.Commit.Signatures = make([]blocks.CommitSig, len(sigs))
	for i, s := range sigs {
		var ts time.Time
		if s.GetTimestampUnixNano() != 0 {
			ts = time.Unix(0, s.GetTimestampUnixNano()).UTC()
		}
		h.Commit.Signatures[i] = blocks.CommitSig{
			ValidatorAddress: append([]byte(nil), s.GetValidatorAddress()...),
			Timestamp:        ts,
			Signature:        append([]byte(nil), s.GetSignature()...),
		}
	}
	return h
}

// ProofToProto maps a BlockOracle proof onto the NodeManager wire type.
func ProofToProto(p *blocks.Proof) *gen.BlockProof {
	if p == nil {
		return nil
	}
	out := &gen.BlockProof{
		Path:  p.Path,
		Value: append([]byte(nil), p.Value...),
	}
	if len(p.Ops) == 0 {
		return out
	}
	out.Ops = make([][]byte, len(p.Ops))
	for i, op := range p.Ops {
		out.Ops[i] = append([]byte(nil), op...)
	}
	return out
}

// ProofFromProto is the inverse of ProofToProto.
func ProofFromProto(p *gen.BlockProof) *blocks.Proof {
	if p == nil {
		return nil
	}
	out := &blocks.Proof{
		Path:  p.GetPath(),
		Value: append([]byte(nil), p.GetValue()...),
	}
	ops := p.GetOps()
	if len(ops) == 0 {
		return out
	}
	out.Ops = make([][]byte, len(ops))
	for i, op := range ops {
		out.Ops[i] = append([]byte(nil), op...)
	}
	return out
}
