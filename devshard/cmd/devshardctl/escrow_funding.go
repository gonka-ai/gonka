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

	"devshard/state"
	"devshard/types"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

type chatRequestCost struct {
	promptTokens     int64
	inputLengthBytes uint64
	maxTokens        uint64
}

func newChatRequestCost(body []byte, req chatRequest, promptTokens int64) chatRequestCost {
	return chatRequestCost{
		promptTokens:     promptTokens,
		inputLengthBytes: uint64(len(body)) + upstreamStreamRewriteMargin,
		maxTokens:        req.MaxTokens,
	}
}

func (cost chatRequestCost) startChargeOn(config types.SessionConfig) (uint64, error) {
	reserved, err := state.ReservedCost(cost.inputLengthBytes, cost.maxTokens, config.TokenPrice)
	if err != nil {
		return 0, err
	}
	if reserved > math.MaxUint64-config.FeePerNonce {
		return 0, types.ErrCostOverflow
	}
	return reserved + config.FeePerNonce, nil
}

func escrowCanFund(rt *devshardRuntime, cost chatRequestCost) bool {
	if rt == nil || rt.proxy == nil || rt.proxy.sm == nil {
		return true
	}
	charge, err := cost.startChargeOn(rt.proxy.sm.Config())
	if err != nil {
		return false
	}
	return rt.proxy.sm.Balance() >= charge
}

const escrowFundingDenom = "ngonka"

var (
	errEscrowFundingInsufficient = errors.New("escrow funding account has insufficient funds")
	gatewaySpendableBalance      = (*Gateway).spendableBalance
)

func gatewayTxFee() (denom string, amount uint64) {
	return firstNonEmpty(os.Getenv("DEVSHARD_TX_FEE_DENOM"), chaintx.DefaultFeeDenom),
		firstNonZeroUint64(uint64(readInt64Env("DEVSHARD_TX_FEE_AMOUNT", int64(chaintx.DefaultFeeAmount))))
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
	if response.Balance == nil || !response.Balance.Amount.IsUint64() {
		return 0, nil
	}
	return response.Balance.Amount.Uint64(), nil
}

func (g *Gateway) ensureCanFundEscrow(ctx context.Context, address string, amount uint64) error {
	feeDenom, feeAmount := gatewayTxFee()
	required := amount
	if feeDenom == escrowFundingDenom {
		required += feeAmount
	}
	spendable, err := gatewaySpendableBalance(g, ctx, address)
	if err != nil {
		log.Printf("escrow_funding_check_skipped address=%s error=%v", address, err)
		return nil
	}
	if spendable < required {
		return fmt.Errorf("%w: %s has %d%s spendable, an escrow needs %d%s", errEscrowFundingInsufficient, address, spendable, escrowFundingDenom, required, escrowFundingDenom)
	}
	return nil
}
