package mockdapi

import (
	"context"
	"fmt"

	"common/chain"
	"common/chainoracle/blocks"

	"devshard/chainoracle/blocks/direct"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// chainBlocks answers the node-manager header lookups from the mock chain, so a host asking for a height it never observed gets the hash and time its peers carry, as it does from a real dapi.
type chainBlocks struct {
	blocks.BlockOracle
}

func newChainBlocks(client *chain.Client) chainBlocks {
	return chainBlocks{BlockOracle: direct.NewFromChain(client)}
}

// At reads a height above the tip as not found, which is how dapi answers it.
func (c chainBlocks) At(ctx context.Context, height int64) (*blocks.Header, error) {
	header, err := c.BlockOracle.At(ctx, height)
	if status.Code(err) == codes.InvalidArgument {
		return nil, fmt.Errorf("%w: %v", blocks.ErrHeaderNotFound, err)
	}
	return header, err
}
