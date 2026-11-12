package grpcface

import (
	"context"
	"strings"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MockWalletBalance is what every wallet can spend: the mock chain never refuses a transaction for money.
const MockWalletBalance uint64 = 1_000_000_000_000_000

// BankServer implements the cosmos.bank.v1beta1 Query a gateway reads before it creates an escrow.
type BankServer struct {
	banktypes.UnimplementedQueryServer
}

func (*BankServer) SpendableBalanceByDenom(_ context.Context, req *banktypes.QuerySpendableBalanceByDenomRequest) (*banktypes.QuerySpendableBalanceByDenomResponse, error) {
	if strings.TrimSpace(req.Address) == "" || strings.TrimSpace(req.Denom) == "" {
		return nil, status.Error(codes.InvalidArgument, "address and denom are required")
	}
	balance := sdk.NewCoin(req.Denom, sdkmath.NewIntFromUint64(MockWalletBalance))
	return &banktypes.QuerySpendableBalanceByDenomResponse{Balance: &balance}, nil
}
