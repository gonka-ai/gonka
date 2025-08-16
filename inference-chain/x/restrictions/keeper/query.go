package keeper

import (
	"github.com/productscience/inference/x/restrictions/types"
)

var _ types.QueryServer = Keeper{}
