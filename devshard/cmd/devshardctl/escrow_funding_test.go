package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"

	chaintx "common/chain/tx"
)

func stubSpendableBalance(t *testing.T, spendable uint64, queryError error) {
	t.Helper()
	saved := gatewaySpendableBalance
	gatewaySpendableBalance = func(*Gateway, context.Context, string) (uint64, error) {
		return spendable, queryError
	}
	t.Cleanup(func() { gatewaySpendableBalance = saved })
}

func TestEnsureCanFundEscrowRequiresTheAmountPlusTheFee(t *testing.T) {
	_, feeAmount := resolveTxFee("", 0)
	for _, testCase := range []struct {
		name       string
		spendable  uint64
		queryError error
		isRefused  bool
	}{
		{name: "amount_plus_fee_is_enough", spendable: 500_000_000 + feeAmount},
		{name: "one_short_of_the_fee_is_refused", spendable: 500_000_000 + feeAmount - 1, isRefused: true},
		{name: "an_unanswered_balance_query_does_not_block_creation", queryError: errors.New("bank query unimplemented")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stubSpendableBalance(t, testCase.spendable, testCase.queryError)

			err := (&Gateway{}).ensureCanPayTx(t.Context(), "gonka1creator", 500_000_000, "", 0)

			if testCase.isRefused {
				require.ErrorIs(t, err, errInsufficientSpendableFunds)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestEnsureCanPayTxCountsTheFeeTheRequestOverrides(t *testing.T) {
	// Test flow:
	// 1. Stub the wallet with exactly 100ngonka spendable.
	// 2. Ask for a zero-amount transaction under each fee override.
	// 3. A fee in ngonka above the balance is refused; the same fee in another denom is not drawn from ngonka.
	stubSpendableBalance(t, 100, nil)
	for _, testCase := range []struct {
		name      string
		feeDenom  string
		feeAmount uint64
		isRefused bool
	}{
		{name: "ngonka_fee_within_balance", feeDenom: escrowFundingDenom, feeAmount: 100},
		{name: "ngonka_fee_over_balance", feeDenom: escrowFundingDenom, feeAmount: 101, isRefused: true},
		{name: "fee_in_another_denom", feeDenom: "uatom", feeAmount: 1_000_000},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := (&Gateway{}).ensureCanPayTx(t.Context(), "gonka1settler", 0, testCase.feeDenom, testCase.feeAmount)

			if testCase.isRefused {
				require.ErrorIs(t, err, errInsufficientSpendableFunds)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSettleDevshardOnChainRefusesBeforeFinalizeWhenTheSettlerCannotPayTheFee(t *testing.T) {
	// Test flow:
	// 1. Register an escrow whose settler wallet has nothing spendable.
	// 2. Settle it with a valid key.
	// 3. The settle is refused as insufficient funds before it reaches Finalize, which would need a live session.
	runtime := &devshardRuntime{id: "88", model: "m"}
	runtime.active.Store(true)
	gateway := newFinalizeTestGateway(t, runtime)
	stubSpendableBalance(t, 0, nil)
	settlerKey := testutil.MustGenerateKey(t).PrivateKeyHex()

	var err error
	require.NotPanics(t, func() {
		_, err = gateway.settleDevshardOnChain(t.Context(), "88", adminSettleEscrowRequest{PrivateKey: settlerKey})
	})

	require.ErrorIs(t, err, errInsufficientSpendableFunds)
	require.Equal(t, "insufficient_funds", settleBatchErrorReason(err))
}

func TestAdminCreateEscrowRefusesWhenTheWalletCannotFundIt(t *testing.T) {
	// Test flow:
	// 1. Stub the creator wallet one ngonka short of the amount plus the fee.
	// 2. POST /v1/admin/escrows for that amount.
	// 3. The gateway answers 402 and never builds a transaction.
	_, feeAmount := resolveTxFee("", 0)
	gateway := newFinalizeTestGateway(t)
	stubSpendableBalance(t, 500_000_000+feeAmount-1, nil)
	body := fmt.Sprintf(`{"amount":500000000,"private_key":%q}`, testutil.MustGenerateKey(t).PrivateKeyHex())
	recorder := httptest.NewRecorder()

	gateway.handleAdminEscrows(recorder, httptest.NewRequest(http.MethodPost, "/v1/admin/escrows", strings.NewReader(body)))

	require.Equal(t, http.StatusPaymentRequired, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), errInsufficientSpendableFunds.Error())
}

func TestResolveTxFeeFallsBackToTheChainDefaultForAZeroFee(t *testing.T) {
	// Test flow:
	// 1. Configure a zero fee amount in the environment.
	// 2. Resolve the fee without request overrides.
	// 3. Expect the chain default the transaction will actually be charged, not zero.
	t.Setenv("DEVSHARD_TX_FEE_AMOUNT", "0")

	feeDenom, feeAmount := resolveTxFee("", 0)

	require.Equal(t, chaintx.DefaultFeeDenom, feeDenom)
	require.Equal(t, chaintx.DefaultFeeAmount, feeAmount)
}

func TestEnsureCanPayTxRefusesAnAmountWhoseFeeOverflows(t *testing.T) {
	// Test flow:
	// 1. Stub a wallet one ngonka short of the largest amount.
	// 2. Ask for the largest amount plus the default ngonka fee.
	// 3. Expect a refusal instead of the sum wrapping to a small number.
	stubSpendableBalance(t, math.MaxUint64-1, nil)

	err := (&Gateway{}).ensureCanPayTx(t.Context(), "gonka1creator", math.MaxUint64, "", 0)

	require.ErrorIs(t, err, errInsufficientSpendableFunds)
}

func TestAdminSettleDevshardAnswers402WhenTheSettlerCannotPayTheFee(t *testing.T) {
	// Test flow:
	// 1. Register an escrow whose settler wallet has nothing spendable.
	// 2. POST its single settle with a valid key.
	// 3. Expect 402, because nothing was finalized or broadcast.
	runtime := &devshardRuntime{id: "89", model: "m"}
	runtime.active.Store(true)
	gateway := newFinalizeTestGateway(t, runtime)
	stubSpendableBalance(t, 0, nil)
	body := fmt.Sprintf(`{"private_key":%q}`, testutil.MustGenerateKey(t).PrivateKeyHex())
	recorder := httptest.NewRecorder()

	gateway.handleAdminSettleDevshard(recorder, httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/89/settle", strings.NewReader(body)), "89")

	require.Equal(t, http.StatusPaymentRequired, recorder.Code, recorder.Body.String())
}
