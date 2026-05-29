package simulation_test

import (
	"encoding/base64"
	"math/rand"
	"testing"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/simulation"
)

func decimalOne() decimal.Decimal { return decimal.NewFromInt(1) }

// Each factory test exercises the message-building closure end-to-end:
// keeper seeded with N Participants → bootstrap + pick run inside the
// factory → ValidateBasic must pass on the produced msg → signer in
// returned []SimAccount must match msg.Creator. This is the unit-level
// contract for these factories. Full simsx hit-rate behavior is covered
// separately by the verification-gate smoke test.

const factoryAccountCount = 5

func TestMsgSubmitNewParticipantFactory_BuildsValidMsg(t *testing.T) {
	kk, sdkCtx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 11, factoryAccountCount)
	// SubmitNewParticipant does not require ActiveParticipantsSet — minimal
	// setup. We still register accounts so they exist as sim accounts.
	cds := simsx.NewChainDataSource(sdkCtx, rand.New(rand.NewSource(12)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	signers, msg := simulation.MsgSubmitNewParticipantFactory(kk)(sdkCtx, cds, reporter)
	require.False(t, reporter.IsSkipped(), "factory skipped unexpectedly")
	require.NotNil(t, msg)
	require.Len(t, signers, 1)
	require.NoError(t, msg.ValidateBasic())
	require.Equal(t, signers[0].AddressBech32, msg.Creator)
}

func TestMsgStartInferenceFactory_BuildsValidMsg(t *testing.T) {
	kk, sdkCtx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 21, factoryAccountCount)
	registerAsParticipants(t, kk, sdkCtx, accs)
	registerSimGenesisModels(t, kk, sdkCtx)
	require.NoError(t, kk.SetEffectiveEpochIndex(sdkCtx, 0))
	cds := simsx.NewChainDataSource(sdkCtx, rand.New(rand.NewSource(22)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	signers, msg := simulation.MsgStartInferenceFactory(kk)(sdkCtx, cds, reporter)
	require.False(t, reporter.IsSkipped(), "factory skipped unexpectedly")
	require.NotNil(t, msg)
	require.Len(t, signers, 1)
	require.NoError(t, msg.ValidateBasic())
	// Same account in all three roles.
	require.Equal(t, signers[0].AddressBech32, msg.Creator)
	require.Equal(t, msg.Creator, msg.RequestedBy)
	require.Equal(t, msg.Creator, msg.AssignedTo)
	// Bootstrap side effect — ActiveParticipantsSet[0] is now non-empty.
	require.NotEmpty(t, collectActiveAddrs(t, sdkCtx, kk, 0))
	// Picked Model is one of the genesis-registered sim models.
	require.Contains(t, simulation.SimModelIDs, msg.Model)
	// Real dev signature in msg.InferenceId. Verify against
	// signer pubkey so the handler's verifyStartFirstMessageKeys would
	// pass.
	devComponents := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.Creator,
	}
	require.NoError(t,
		calculations.ValidateSignature(devComponents, calculations.Developer,
			pubKeyB64(t, signers[0].Account), msg.InferenceId))
}

// TestMsgFinishInferenceFactory_FinishFirst_BuildsValidMsg — with no
// STARTED inference in keeper, the factory takes path 2 (fresh
// finish-first) and produces a msg with real dev + TA signatures.
func TestMsgFinishInferenceFactory_FinishFirst_BuildsValidMsg(t *testing.T) {
	kk, sdkCtx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 31, factoryAccountCount)
	registerAsParticipants(t, kk, sdkCtx, accs)
	registerSimGenesisModels(t, kk, sdkCtx)
	require.NoError(t, kk.SetEffectiveEpochIndex(sdkCtx, 0))
	cds := simsx.NewChainDataSource(sdkCtx, rand.New(rand.NewSource(32)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	signers, msg := simulation.MsgFinishInferenceFactory(kk)(sdkCtx, cds, reporter)
	require.False(t, reporter.IsSkipped(), "factory skipped unexpectedly")
	require.NotNil(t, msg)
	require.Len(t, signers, 1)
	require.NoError(t, msg.ValidateBasic())
	// Creator == ExecutedBy (hard at msg_server_finish_inference.go)
	// and same account occupies all four roles.
	require.Equal(t, signers[0].AddressBech32, msg.Creator)
	require.Equal(t, msg.Creator, msg.ExecutedBy)
	require.Equal(t, msg.Creator, msg.TransferredBy)
	require.Equal(t, msg.Creator, msg.RequestedBy)
	// Picked Model is one of the genesis-registered sim models.
	require.Contains(t, simulation.SimModelIDs, msg.Model)
	// Real dev signature in msg.InferenceId.
	devComponents := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
	}
	require.NoError(t,
		calculations.ValidateSignature(devComponents, calculations.Developer,
			pubKeyB64(t, signers[0].Account), msg.InferenceId))
	// Real TA signature in msg.TransferSignature.
	taComponents := calculations.SignatureComponents{
		Payload:         msg.PromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: msg.ExecutedBy,
	}
	require.NoError(t,
		calculations.ValidateSignature(taComponents, calculations.TransferAgent,
			pubKeyB64(t, signers[0].Account), msg.TransferSignature))
}

// TestMsgFinishInferenceFactory_PairsStartedInference — with a
// STARTED inference present whose AssignedTo is a sim account, the
// factory takes path 1 and finishes THAT inference, copying its
// InferenceId and the dev/TA components the start-first compare path
// checks (compareDevComponents / compareFinishTAComponents).
func TestMsgFinishInferenceFactory_PairsStartedInference(t *testing.T) {
	kk, sdkCtx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 33, factoryAccountCount)
	regAddrs := registerAsParticipants(t, kk, sdkCtx, accs)
	registerSimGenesisModels(t, kk, sdkCtx)
	require.NoError(t, kk.SetEffectiveEpochIndex(sdkCtx, 0))
	// InferenceId must be base64 of 64 bytes (utils.ValidateBase64RSig64,
	// enforced by MsgFinishInference.ValidateBasic). Real Start inferences
	// carry a 64-byte dev signature here; the test uses a fixed-byte
	// stand-in.
	startedID := base64.StdEncoding.EncodeToString(make([]byte, 64))
	assignedTo := regAddrs[2]
	putStartedInference(t, kk, sdkCtx, startedID, assignedTo, simulation.SimModelIDs[0])
	cds := simsx.NewChainDataSource(sdkCtx, rand.New(rand.NewSource(34)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	signers, msg := simulation.MsgFinishInferenceFactory(kk)(sdkCtx, cds, reporter)
	require.False(t, reporter.IsSkipped(), "factory skipped unexpectedly")
	require.NotNil(t, msg)
	require.Len(t, signers, 1)
	require.NoError(t, msg.ValidateBasic())
	// Finishes the seeded STARTED inference verbatim.
	require.Equal(t, startedID, msg.InferenceId)
	require.Equal(t, assignedTo, msg.Creator)
	require.Equal(t, assignedTo, msg.ExecutedBy)
	require.Equal(t, assignedTo, signers[0].AddressBech32)
	// dev/TA components copied from the inference (compare-path inputs).
	require.Equal(t, "sim-prompt-hash", msg.PromptHash)
	require.Equal(t, "sim-original-prompt-hash", msg.OriginalPromptHash)
	require.Equal(t, simulation.SimModelIDs[0], msg.Model)
	require.Equal(t, int64(1_000_000), msg.RequestTimestamp)
}

// TestMsgValidationFactory_PicksExistingInference — with a pre-seeded
// Inference in keeper whose ExecutedBy is one of the active sim
// participants, the factory picks a DIFFERENT active sim participant as
// validator (validator!=executor constraint) and produces a
// ValidateBasic-passing msg referencing the seeded InferenceId.
func TestMsgValidationFactory_PicksExistingInference(t *testing.T) {
	kk, sdkCtx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 41, factoryAccountCount)
	regAddrs := registerAsParticipants(t, kk, sdkCtx, accs)
	require.NoError(t, kk.SetEffectiveEpochIndex(sdkCtx, 0))
	seedActiveParticipantsForTest(t, sdkCtx, kk, accs, 0)
	const seededID = "sim-inference-001"
	executor := regAddrs[0]
	putFinishedInference(t, kk, sdkCtx, seededID, executor)
	// MsgValidationFactory consults the per-model transient weight cache
	// (GetCachedEpochDataModelWeight) before drawing a validator; seed it
	// for the active participants so a real validator is eligible.
	seedModelWeightCacheForTest(t, sdkCtx, kk, regAddrs, 0)
	cds := simsx.NewChainDataSource(sdkCtx, rand.New(rand.NewSource(42)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	signers, msg := simulation.MsgValidationFactory(kk).SimMsgFactoryFn(sdkCtx, cds, reporter)
	require.False(t, reporter.IsSkipped(), "factory skipped unexpectedly: %s", reporter.Comment())
	require.NotNil(t, msg)
	require.Len(t, signers, 1)
	require.NoError(t, msg.ValidateBasic())
	require.Equal(t, signers[0].AddressBech32, msg.Creator)
	require.Equal(t, seededID, msg.InferenceId)
	require.NotEqual(t, executor, msg.Creator,
		"validator must not equal executor (msg_server_validation.go)")
	require.NotNil(t, msg.ValueDecimal)
	v := msg.ValueDecimal.ToDecimal()
	require.False(t, v.IsNegative())
	require.True(t, v.LessThanOrEqual(decimalOne()))
}

// TestMsgValidationFactory_EmptyFinishedInferences_Skips — no FINISHED
// inferences in keeper ⇒ reporter Skipped, no msg produced. Even if
// STARTED inferences exist they're filtered out (factory uses
// PickRandomFinishedInference so MsgValidation handler's
// ErrInferenceNotFinished path at line 77-79 is never reached).
func TestMsgValidationFactory_EmptyFinishedInferences_Skips(t *testing.T) {
	kk, sdkCtx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 43, factoryAccountCount)
	registerAsParticipants(t, kk, sdkCtx, accs)
	require.NoError(t, kk.SetEffectiveEpochIndex(sdkCtx, 0))
	cds := simsx.NewChainDataSource(sdkCtx, rand.New(rand.NewSource(44)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	_, msg := simulation.MsgValidationFactory(kk).SimMsgFactoryFn(sdkCtx, cds, reporter)
	require.Nil(t, msg)
	require.True(t, reporter.IsSkipped(), "expected Skip on empty Inferences")
}

func TestMsgClaimRewardsFactory_BuildsValidMsg(t *testing.T) {
	kk, sdkCtx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 51, factoryAccountCount)
	registerAsParticipants(t, kk, sdkCtx, accs)
	require.NoError(t, kk.SetEffectiveEpochIndex(sdkCtx, 0))
	cds := simsx.NewChainDataSource(sdkCtx, rand.New(rand.NewSource(52)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	signers, msg := simulation.MsgClaimRewardsFactory(kk)(sdkCtx, cds, reporter)
	require.False(t, reporter.IsSkipped(), "factory skipped unexpectedly")
	require.NotNil(t, msg)
	require.Len(t, signers, 1)
	require.NoError(t, msg.ValidateBasic())
	require.Equal(t, signers[0].AddressBech32, msg.Creator)
	require.Greater(t, msg.EpochIndex, uint64(0)) // ValidateBasic requirement
}
