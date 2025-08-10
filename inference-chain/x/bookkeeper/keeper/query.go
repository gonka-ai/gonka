package keeper

import (
	"github.com/productscience/inference/x/bookkeeper/types"
)

var _ types.QueryServer = Keeper{}
