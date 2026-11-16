package observer

import (
	"context"
	"fmt"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"

	cmttypes "github.com/cometbft/cometbft/types"
)

// LightBlockAt serves evidence, including historical bootstrap/transition
// blocks outside the header cache. The recipient must authenticate it against
// its own pinned checkpoint; a self-consistent commit is not a trust anchor.
func (o *Oracle) LightBlockAt(ctx context.Context, height int64) ([]byte, error) {
	if o == nil || height <= 0 {
		return nil, blocks.ErrHeaderNotFound
	}
	rpc, ok := o.rpc.(lightRPC)
	if !ok {
		return nil, blocks.ErrProveNotImplemented
	}
	sh, vals, err := newLightVerifier(rpc, 0).selfVerified(ctx, &height)
	if err != nil {
		return nil, err
	}
	if sh.Height != height {
		return nil, fmt.Errorf("%w: requested %d, received %d", blocks.ErrHeaderUnverified, height, sh.Height)
	}
	pb, err := (&cmttypes.LightBlock{SignedHeader: sh, ValidatorSet: vals}).ToProto()
	if err != nil {
		return nil, err
	}
	raw, err := pb.Marshal()
	if err == nil && len(raw) > lightproof.MaxBlockBytes {
		err = fmt.Errorf("light block exceeds %d bytes", lightproof.MaxBlockBytes)
	}
	return raw, err
}

var _ lightproof.Source = (*Oracle)(nil)
