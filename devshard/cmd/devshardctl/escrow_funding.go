package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strings"

	chaintx "common/chain/tx"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

const escrowFundingDenom = "ngonka"

var (
	errInsufficientSpendableFunds = errors.New("account has insufficient spendable funds")
	gatewaySpendableBalance       = (*Gateway).spendableBalance
)

func resolveTxFee(denomOverride string, amountOverride uint64) (denom string, amount uint64) {
	return firstNonEmpty(denomOverride, os.Getenv("DEVSHARD_TX_FEE_DENOM"), chaintx.DefaultFeeDenom),
		firstNonZeroUint64(amountOverride, uint64(readInt64Env("DEVSHARD_TX_FEE_AMOUNT", int64(chaintx.DefaultFeeAmount))), chaintx.DefaultFeeAmount)
}

func (g *Gateway) spendableBalance(ctx context.Context, address string) (uint64, error) {
	if g.chainClient == nil {
		return 0, fmt.Errorf("chain gRPC client is not configured")
	}
	response, err := banktypes.NewQueryClient(g.chainClient.QueryConn()).SpendableBalanceByDenom(ctx, &banktypes.QuerySpendableBalanceByDenomRequest{
		Address: strings.TrimSpace(address),
		Denom:   escrowFundingDenom,
	})
	if err != nil {
		return 0, err
	}
	if response.Balance == nil {
		return 0, nil
	}
	if !response.Balance.Amount.IsUint64() {
		return math.MaxUint64, nil
	}
	return response.Balance.Amount.Uint64(), nil
}

func (g *Gateway) ensureCanPayTx(ctx context.Context, address string, amount uint64, feeDenomOverride string, feeAmountOverride uint64) error {
	feeDenom, feeAmount := resolveTxFee(feeDenomOverride, feeAmountOverride)
	required := amount
	if feeDenom == escrowFundingDenom {
		required = amount + min(feeAmount, math.MaxUint64-amount)
	}
	spendable, err := gatewaySpendableBalance(g, ctx, address)
	if err != nil {
		log.Printf("tx_funding_check_skipped address=%s error=%v", address, err)
		return nil
	}
	if spendable < required {
		return fmt.Errorf("%w: %s has %d%s spendable, the transaction needs %d%s", errInsufficientSpendableFunds, address, spendable, escrowFundingDenom, required, escrowFundingDenom)
	}
	return nil
}
