package mockdapi

import (
	"context"
	"fmt"

	cblocks "common/chainoracle/blocks"
	"devshard/chainoracle/blocks"
)

// commonBlockOracle adapts the mock observer onto the common BlockOracle
// the NodeManager gRPC handlers already speak.
type commonBlockOracle struct {
	inner blocks.BlockOracle
}

func (o commonBlockOracle) Latest(ctx context.Context) (*cblocks.Header, error) {
	h, err := o.inner.Latest(ctx)
	if err != nil {
		return nil, mapMockOracleErr(ctx, err)
	}
	return toCommonHeader(h), nil
}

func (o commonBlockOracle) At(ctx context.Context, height int64) (*cblocks.Header, error) {
	h, err := o.inner.At(ctx, height)
	if err != nil {
		return nil, mapMockOracleErr(ctx, err)
	}
	return toCommonHeader(h), nil
}

func (o commonBlockOracle) Prove(ctx context.Context, path string, height int64) (*cblocks.Proof, error) {
	p, err := o.inner.Prove(ctx, path, height)
	if err != nil {
		return nil, mapMockOracleErr(ctx, err)
	}
	return toCommonProof(p), nil
}

func (o commonBlockOracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *cblocks.Header, error) {
	ch, err := o.inner.Subscribe(ctx, fromHeight)
	if err != nil {
		return nil, err
	}
	out := make(chan *cblocks.Header)
	go func() {
		defer close(out)
		for h := range ch {
			select {
			case <-ctx.Done():
				return
			case out <- toCommonHeader(h):
			}
		}
	}()
	return out, nil
}

func mapMockOracleErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %v", cblocks.ErrHeaderNotFound, err)
}

func toCommonHeader(h *blocks.Header) *cblocks.Header {
	if h == nil {
		return nil
	}
	out := &cblocks.Header{
		Height:             h.Height,
		Time:               h.Time,
		ChainID:            h.ChainID,
		BlockHash:          append([]byte(nil), h.BlockHash...),
		AppHash:            append([]byte(nil), h.AppHash...),
		ValidatorsHash:     append([]byte(nil), h.ValidatorsHash...),
		NextValidatorsHash: append([]byte(nil), h.NextValidatorsHash...),
		Commit: cblocks.Commit{
			Height:  h.Commit.Height,
			Round:   h.Commit.Round,
			BlockID: append([]byte(nil), h.Commit.BlockID...),
		},
	}
	if len(h.Commit.Signatures) == 0 {
		return out
	}
	out.Commit.Signatures = make([]cblocks.CommitSig, len(h.Commit.Signatures))
	for i, s := range h.Commit.Signatures {
		out.Commit.Signatures[i] = cblocks.CommitSig{
			ValidatorAddress: append([]byte(nil), s.ValidatorAddress...),
			Timestamp:        s.Timestamp,
			Signature:        append([]byte(nil), s.Signature...),
		}
	}
	return out
}

func toCommonProof(p *blocks.Proof) *cblocks.Proof {
	if p == nil {
		return nil
	}
	out := &cblocks.Proof{
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

var _ cblocks.BlockOracle = commonBlockOracle{}
