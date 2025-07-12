package keeper

import (
	"github.com/productscience/inference/x/collateral/types"
)

var _ types.QueryServer = Keeper{}
