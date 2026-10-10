package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// GetAccountPubKeysWithGranteesForTest exposes the unexported msgServer method
// (defined as type msgServer struct { Keeper }) for external keeper tests.
func GetAccountPubKeysWithGranteesForTest(ms types.MsgServer, ctx context.Context, granter string) ([]string, error) {
	return ms.(*msgServer).GetAccountPubKeysWithGrantees(ctx, granter)
}
