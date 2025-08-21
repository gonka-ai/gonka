package keeper

import (
	"github.com/productscience/inference/x/genesistransfer/types"
)

var _ types.QueryServer = Keeper{}
