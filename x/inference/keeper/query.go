package keeper

import (
	"github.com/productscience/inference/x/inference/types"
)

var _ types.QueryServer = Keeper{}
