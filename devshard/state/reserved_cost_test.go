package state

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// The gateway routes on ReservedCost before an escrow ever sees the request, so the number it
// predicts must be the number StartInference takes off the balance.
func TestReservedCostIsWhatStartInferenceTakesOffTheBalance(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	config.TokenPrice = 7
	const startingBalance = 1_000_000
	verifier := signing.NewSecp256k1Verifier()
	sm, err := NewStateMachine("escrow-1", config, group, startingBalance, user.Address(), verifier,
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, startingBalance))
	require.NoError(t, err)

	const inputLength = 100
	predicted, err := ReservedCost(inputLength, testutil.TestMaxTokens, config.TokenPrice)
	require.NoError(t, err)

	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: inputLength,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	})}))
	require.NoError(t, err)

	require.EqualValues(t, startingBalance-predicted, sm.Balance(),
		"the balance a routing decision predicts must be the balance the reservation leaves")
}

func TestReservedCostReportsOverflowInsteadOfWrapping(t *testing.T) {
	_, err := ReservedCost(math.MaxUint64, 1, 1)
	require.ErrorIs(t, err, types.ErrCostOverflow)

	_, err = ReservedCost(math.MaxUint64/2, 0, 3)
	require.ErrorIs(t, err, types.ErrCostOverflow)
}
