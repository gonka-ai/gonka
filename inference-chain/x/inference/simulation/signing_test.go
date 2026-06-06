package simulation_test

import (
	"encoding/base64"
	"math/rand"
	"testing"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/simulation"
	"github.com/productscience/inference/x/inference/types"
)

// pubKeyB64 returns the standard-base64 encoding of the secp256k1 pubkey
// bytes — the same format the on-chain GetAccountPubKey returns
// (msg_server_start_inference.go).
func pubKeyB64(t *testing.T, a simtypes.Account) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(a.PubKey.Bytes())
}

// TestSignDevStart_RoundTrip — SignDevStart's output decodes and
// ValidateSignature(Developer) accepts it against the signing account's
// pubkey. This verifies the helper plugs cleanly into the on-chain
// VerifyKeys path used by verifyStartFirstMessageKeys.
func TestSignDevStart_RoundTrip(t *testing.T) {
	accs := simtypes.RandomAccounts(rand.New(rand.NewSource(1)), 1)
	sa := simsx.SimAccount{Account: accs[0]}

	msg := &types.MsgStartInference{
		Creator:            accs[0].Address.String(),
		RequestedBy:        accs[0].Address.String(),
		AssignedTo:         accs[0].Address.String(),
		OriginalPromptHash: "orig-hash",
		PromptHash:         "prompt-hash",
		Model:              "sim-model",
		RequestTimestamp:   1234567890,
	}

	sig, err := simulation.SignDevStart(sa, msg)
	require.NoError(t, err)
	require.NotEmpty(t, sig)

	components := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.Creator,
	}
	require.NoError(t,
		calculations.ValidateSignature(components, calculations.Developer,
			pubKeyB64(t, accs[0]), sig))
}

// TestSignDevFinish_RoundTrip — same for Finish dev signature, using
// MsgFinishInference's TransferredBy as TransferAddress.
func TestSignDevFinish_RoundTrip(t *testing.T) {
	accs := simtypes.RandomAccounts(rand.New(rand.NewSource(2)), 1)
	sa := simsx.SimAccount{Account: accs[0]}

	msg := &types.MsgFinishInference{
		Creator:            accs[0].Address.String(),
		ExecutedBy:         accs[0].Address.String(),
		TransferredBy:      accs[0].Address.String(),
		RequestedBy:        accs[0].Address.String(),
		OriginalPromptHash: "orig-hash-finish",
		PromptHash:         "prompt-hash-finish",
		Model:              "sim-model",
		RequestTimestamp:   2222222222,
	}

	sig, err := simulation.SignDevFinish(sa, msg)
	require.NoError(t, err)
	require.NotEmpty(t, sig)

	components := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
	}
	require.NoError(t,
		calculations.ValidateSignature(components, calculations.Developer,
			pubKeyB64(t, accs[0]), sig))
}

// TestSignTAFinish_RoundTrip — TA signature includes ExecutorAddress in
// the byte payload (getTransferBytes appends ExecutorAddress on top of
// getDevBytes). Verified via ValidateSignature(TransferAgent).
func TestSignTAFinish_RoundTrip(t *testing.T) {
	accs := simtypes.RandomAccounts(rand.New(rand.NewSource(3)), 1)
	sa := simsx.SimAccount{Account: accs[0]}

	msg := &types.MsgFinishInference{
		Creator:          accs[0].Address.String(),
		ExecutedBy:       accs[0].Address.String(),
		TransferredBy:    accs[0].Address.String(),
		PromptHash:       "ta-prompt-hash",
		RequestTimestamp: 3333333333,
	}

	sig, err := simulation.SignTAFinish(sa, msg)
	require.NoError(t, err)
	require.NotEmpty(t, sig)

	components := calculations.SignatureComponents{
		Payload:         msg.PromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: msg.ExecutedBy,
	}
	require.NoError(t,
		calculations.ValidateSignature(components, calculations.TransferAgent,
			pubKeyB64(t, accs[0]), sig))
}

// TestSignDevStart_Deterministic — same key + same components → same
// signature. cosmos-sdk secp256k1 uses deterministic ECDSA per RFC 6979,
// so the helper must reproduce bit-identically. This guarantees
// replay-determinism across simulator seeds.
func TestSignDevStart_Deterministic(t *testing.T) {
	accs := simtypes.RandomAccounts(rand.New(rand.NewSource(4)), 1)
	sa := simsx.SimAccount{Account: accs[0]}

	msg := &types.MsgStartInference{
		Creator:            accs[0].Address.String(),
		RequestedBy:        accs[0].Address.String(),
		OriginalPromptHash: "det-hash",
		RequestTimestamp:   9999999999,
	}

	sig1, err := simulation.SignDevStart(sa, msg)
	require.NoError(t, err)
	sig2, err := simulation.SignDevStart(sa, msg)
	require.NoError(t, err)
	require.Equal(t, sig1, sig2)
}
