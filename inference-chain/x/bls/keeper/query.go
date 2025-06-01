package keeper

import (
	"github.com/productscience/inference/x/bls/types"
)

var _ types.QueryServer = Keeper{}
