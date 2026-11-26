package failover

import (
	"context"
	"errors"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
)

// LightBlockAt does not apply the short At lookback window: escrow bootstrap
// and validator transitions can precede that cache. Every caller authenticates
// the returned evidence against its own checkpoint.
func (o *Oracle) LightBlockAt(ctx context.Context, height int64) ([]byte, error) {
	if o == nil || height <= 0 {
		return nil, blocks.ErrHeaderNotFound
	}
	var failures []error
	for _, candidate := range []blocks.BlockOracle{o.nm, o.chain} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source, ok := candidate.(lightproof.Source)
		if !ok {
			continue
		}
		raw, err := source.LightBlockAt(ctx, height)
		if err == nil && len(raw) > 0 && len(raw) <= lightproof.MaxBlockBytes {
			return raw, nil
		}
		if err == nil {
			err = blocks.ErrHeaderUnverified
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil, blocks.ErrProveNotImplemented
	}
	return nil, errors.Join(failures...)
}
