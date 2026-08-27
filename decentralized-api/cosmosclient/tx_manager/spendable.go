package tx_manager

import (
	"context"
	"errors"
	"strings"
	"time"

	"cosmossdk.io/math"
	"cosmossdk.io/x/feegrant"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// BankBalancesFn is a fallback when SpendableBalances is unavailable.
type BankBalancesFn func(ctx context.Context, address string) ([]sdk.Coin, error)

// FeePayerSpendable is bank spendable for payer, capped by feegrant when
// signer (grantee) differs from payer (granter). Missing allowance is 0.
func FeePayerSpendable(
	ctx context.Context,
	clientCtx client.Context,
	payer, signer string,
	now time.Time,
	bankFallback BankBalancesFn,
) (math.Int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	spendable, err := querySpendable(ctx, clientCtx, payer, bankFallback)
	if err != nil {
		return math.ZeroInt(), err
	}
	if signer == "" || signer == payer {
		return spendable, nil
	}
	remaining, err := queryFeegrantRemaining(ctx, clientCtx, payer, signer, now)
	if err != nil {
		return math.ZeroInt(), err
	}
	return applyFeegrantCap(spendable, remaining), nil
}

func querySpendable(ctx context.Context, clientCtx client.Context, addr string, bankFallback BankBalancesFn) (math.Int, error) {
	var spendableErr error
	if clientCtx.GRPCClient != nil {
		qc := banktypes.NewQueryClient(clientCtx)
		resp, err := qc.SpendableBalances(ctx, &banktypes.QuerySpendableBalancesRequest{Address: addr})
		if err == nil && resp != nil {
			return ngonkaOf(resp.Balances), nil
		}
		spendableErr = err
		if bankFallback == nil {
			if err != nil {
				return math.ZeroInt(), err
			}
			return math.ZeroInt(), nil
		}
	}
	if bankFallback == nil {
		if spendableErr != nil {
			return math.ZeroInt(), spendableErr
		}
		return math.ZeroInt(), errors.New("no spendable query client or bank fallback")
	}
	coins, err := bankFallback(ctx, addr)
	if err != nil {
		if spendableErr != nil {
			return math.ZeroInt(), spendableErr
		}
		return math.ZeroInt(), err
	}
	return ngonkaOf(sdk.NewCoins(coins...)), nil
}

func queryFeegrantRemaining(ctx context.Context, clientCtx client.Context, granter, grantee string, now time.Time) (math.Int, error) {
	qc := feegrant.NewQueryClient(clientCtx)
	resp, err := qc.Allowance(ctx, &feegrant.QueryAllowanceRequest{Granter: granter, Grantee: grantee})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return math.ZeroInt(), nil
		}
		return math.ZeroInt(), err
	}
	if resp == nil || resp.Allowance == nil || resp.Allowance.Allowance == nil {
		return math.ZeroInt(), nil
	}
	if clientCtx.Codec == nil {
		return math.ZeroInt(), errors.New("client context codec is nil")
	}
	var allowance feegrant.FeeAllowanceI
	if err := clientCtx.Codec.UnpackAny(resp.Allowance.Allowance, &allowance); err != nil {
		return math.ZeroInt(), err
	}
	return feegrantRemaining(allowance, now), nil
}

func applyFeegrantCap(spendable, remaining math.Int) math.Int {
	if remaining.IsNegative() {
		return spendable
	}
	if remaining.LT(spendable) {
		return remaining
	}
	return spendable
}
