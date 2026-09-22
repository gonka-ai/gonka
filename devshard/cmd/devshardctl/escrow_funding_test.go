package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
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
	_, feeAmount := gatewayTxFee()
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

			err := (&Gateway{}).ensureCanFundEscrow(t.Context(), "gonka1creator", 500_000_000)

			if testCase.isRefused {
				require.ErrorIs(t, err, errEscrowFundingInsufficient)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestPrepareBridgeEscrowsSharesTheFundsAcrossModelsRoundRobin(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "first",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{
				{ModelID: "first", TempCount: 3, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY"},
				{ModelID: "second", TempCount: 3, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY"},
				{ModelID: "third", TempCount: 3, TargetCount: 3, Amount: 1000, PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY"},
			},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, nil))

	var createdMutex sync.Mutex
	createdByModel := map[string]int{}
	fundedEscrows := 4
	saved := gatewayCreateRotationEscrow
	gatewayCreateRotationEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, model EscrowRotationModelSettings, _ string, _ uint64) (*CreateDevshardEscrowResult, error) {
		createdMutex.Lock()
		defer createdMutex.Unlock()
		if fundedEscrows == 0 {
			return nil, errEscrowFundingInsufficient
		}
		fundedEscrows--
		createdByModel[model.ModelID]++
		return &CreateDevshardEscrowResult{EscrowID: 1, TxHash: "OK"}, nil
	}
	t.Cleanup(func() { gatewayCreateRotationEscrow = saved })

	gateway := &Gateway{store: store, rotationBreakers: make(map[string]*rotationBreaker)}
	gateway.prepareBridgeEscrows(t.Context(), ChainPhaseSnapshot{EpochIndex: 10}, settings)

	require.Equal(t, map[string]int{"first": 2, "second": 1, "third": 1}, createdByModel,
		"four funded escrows must be spread one per model per pass, not spent on the first model")
}
