package keeper

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/productscience/inference/x/inference/types"
)

func feeGroupGasKey(name string) []byte {
	return append(append([]byte{}, types.TransientFeeGroupGasPrefix...), name...)
}

// BlockFeeGroupGasUsed returns declared gas already reserved for name in this
// block. A missing counter is zero. The transient store clears it at commit.
func (k Keeper) BlockFeeGroupGasUsed(ctx context.Context, name string) (uint64, error) {
	bz, err := k.transientStoreService.OpenTransientStore(ctx).Get(feeGroupGasKey(name))
	if err != nil {
		return 0, err
	}
	if len(bz) == 0 {
		return 0, nil
	}
	if len(bz) != 8 {
		return 0, fmt.Errorf("fee group %s gas counter has length %d", name, len(bz))
	}
	return binary.BigEndian.Uint64(bz), nil
}

// SetBlockFeeGroupGasUsed records declared gas reserved for name in this block.
func (k Keeper) SetBlockFeeGroupGasUsed(ctx context.Context, name string, used uint64) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], used)
	return k.transientStoreService.OpenTransientStore(ctx).Set(feeGroupGasKey(name), buf[:])
}
