package simulation

import (
	"context"
	"encoding/base64"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/shopspring/decimal"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Phase 2 first-wave message factories for x/inference.
//
// Each factory returns a simsx.SimMsgFactoryFn[*types.MsgX] that, when
// invoked, builds a single randomized but valid message instance plus the
// signing accounts. The simsx runner handles tx assembly, signing, and
// delivery — factories only produce the message.
//
// Each factory first runs the sim-only Ensure* bootstrap helpers
// (bootstrap.go) so the message's on-chain preconditions hold, then
// generates a message that clears ValidateBasic, the permission gate and
// signature verification — so the tx reaches real keeper state mutation
// instead of being rejected.

// MsgSubmitNewParticipantFactory creates a factory for MsgSubmitNewParticipant.
//
// MsgSubmitNewParticipant has all-optional fields except Creator
// (ValidateBasic: types/message_submit_new_participant.go). Minimal valid
// msg = Creator only. Handler (keeper/msg_server_submit_new_participant.go)
// is idempotent: if a participant with this address already exists, the msg
// becomes a no-op update (all optional fields empty). On a fresh address a
// new participant is created. Either path exercises the registration
// permission check (OpenRegistrationPermission); if registration is closed
// at this block height the tx will fail with ErrNewParticipantRegistrationClosed
// — that is a legitimate simulation outcome, not a factory bug.
func MsgSubmitNewParticipantFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgSubmitNewParticipant] {
	return func(_ context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgSubmitNewParticipant) {
		from := testData.AnyAccount(reporter)
		if reporter.IsSkipped() {
			return nil, nil
		}
		msg := &types.MsgSubmitNewParticipant{
			Creator: from.Address.String(),
		}
		return []simsx.SimAccount{from}, msg
	}
}

// MsgStartInferenceFactory creates a factory for MsgStartInference.
//
// Real-ECDSA path. Produces a ValidateBasic-passing msg with a
// real ActiveParticipant as Creator (= RequestedBy = AssignedTo — same
// sim account occupies all three roles, so the dev pubkey lookup matches
// the TA). msg.InferenceId is a real secp256k1 dev signature of
// (OriginalPromptHash || RequestTimestamp || Creator) produced by
// SignDevStart, so the handler's verifyStartFirstMessageKeys
// (msg_server_start_inference.go) passes its dev-signature check and
// the inference is actually persisted via SetInference (line 133),
// unblocking paired Finish/Validation state flow.
//
// TransferSignature is still a random 64-byte base64 payload — it is
// not verified on the start-first path (deferred to FinishInference,
// see msg_server_start_inference.go policy comment).
//
// Exercises: ValidateBasic, CheckPermission(ActiveParticipantPermission),
// developer/transfer-agent access gating, GetParticipant lookup,
// timestamp window, dev signature verification (PASS), participant
// access gating, ProcessStartInference, RecordInferencePrice, SetInference,
// addTimeout. Result: inference lands in keeper.
func MsgStartInferenceFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgStartInference] {
	return func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgStartInference) {
		if err := EnsureActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("active-participants bootstrap failed: %v", err)
			return nil, nil
		}
		if err := EnsureComputeValidators(ctx, k); err != nil {
			reporter.Skipf("compute-validators refresh failed: %v", err)
			return nil, nil
		}
		if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
			reporter.Skipf("models epoch-group seeding failed: %v", err)
			return nil, nil
		}
		ta, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}
		modelID, ok := PickRandomGovernanceModelID(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}

		sdkCtx := sdk.UnwrapSDKContext(ctx)
		timestamp := sdkCtx.BlockTime().UnixNano()

		transferSigBytes := make([]byte, 64)
		testData.Rand().Read(transferSigBytes)
		promptHashBytes := make([]byte, 32)
		testData.Rand().Read(promptHashBytes)
		originalPromptHashBytes := make([]byte, 32)
		testData.Rand().Read(originalPromptHashBytes)

		msg := &types.MsgStartInference{
			Creator:            ta.AddressBech32,
			RequestedBy:        ta.AddressBech32,
			AssignedTo:         ta.AddressBech32,
			TransferSignature:  base64.StdEncoding.EncodeToString(transferSigBytes),
			PromptHash:         base64.StdEncoding.EncodeToString(promptHashBytes),
			OriginalPromptHash: base64.StdEncoding.EncodeToString(originalPromptHashBytes),
			Model:              modelID,
			PromptPayload:      "sim-prompt-payload",
			RequestTimestamp:   timestamp,
			MaxTokens:          uint64(testData.Rand().IntInRange(1, 1024)),
			PromptTokenCount:   uint64(testData.Rand().IntInRange(1, 512)),
		}
		devSig, err := SignDevStart(ta, msg)
		if err != nil {
			reporter.Skipf("SignDevStart failed: %v", err)
			return nil, nil
		}
		msg.InferenceId = devSig
		return []simsx.SimAccount{ta}, msg
	}
}

// MsgFinishInferenceFactory creates a factory for MsgFinishInference.
//
// Two paths, chosen by keeper state:
//
//  1. start-first pairing: when a STARTED-status inference
//     exists whose AssignedTo is a known sim account, the factory
//     finishes THAT inference — copying its InferenceId and dev/TA
//     components so the handler's StartProcessed() branch
//     (msg_server_finish_inference.go) takes the compare-only path
//     (compareDevComponents / compareFinishTAComponents /
//     compareFinishModelField) and transitions the inference
//     STARTED → FINISHED. This is what produces FINISHED inferences
//     for MsgValidation to act on. Signatures are NOT re-verified on
//     this path, so InferenceId is reused verbatim and Transfer/Executor
//     signatures are random-but-shape-valid.
//
//  2. fresh finish-first: when no STARTED inference qualifies,
//     the factory builds a brand-new finish-first message with real
//     secp256k1 dev (SignDevFinish) + TA (SignTAFinish) signatures so
//     the handler's verifyFinishKeys path (line 102) passes its
//     cryptographic check.
//
// ExecutorSignature is always random 64-byte base64 — executor signature
// verification is disabled by policy on both paths
// (msg_server_finish_inference.go).
//
// Creator == ExecutedBy is a hard invariant (line 28-32); the same sim
// account occupies all four address roles.
func MsgFinishInferenceFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgFinishInference] {
	return func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgFinishInference) {
		if err := EnsureActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("active-participants bootstrap failed: %v", err)
			return nil, nil
		}
		if err := EnsureComputeValidators(ctx, k); err != nil {
			reporter.Skipf("compute-validators refresh failed: %v", err)
			return nil, nil
		}
		if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
			reporter.Skipf("models epoch-group seeding failed: %v", err)
			return nil, nil
		}

		// Path 1: pair with an existing STARTED inference.
		if started, ok := FindRandomStartedInference(ctx, k, testData); ok {
			executor := testData.GetAccount(reporter, started.AssignedTo)
			if reporter.IsSkipped() {
				return nil, nil
			}
			executorSigBytes := make([]byte, 64)
			testData.Rand().Read(executorSigBytes)
			transferSigBytes := make([]byte, 64)
			testData.Rand().Read(transferSigBytes)
			responseHashBytes := make([]byte, 32)
			testData.Rand().Read(responseHashBytes)
			msg := &types.MsgFinishInference{
				Creator:              started.AssignedTo,
				ExecutedBy:           started.AssignedTo,
				TransferredBy:        started.TransferredBy,
				RequestedBy:          started.RequestedBy,
				InferenceId:          started.InferenceId,
				TransferSignature:    base64.StdEncoding.EncodeToString(transferSigBytes),
				ExecutorSignature:    base64.StdEncoding.EncodeToString(executorSigBytes),
				ResponseHash:         base64.StdEncoding.EncodeToString(responseHashBytes),
				PromptHash:           started.PromptHash,
				OriginalPromptHash:   started.OriginalPromptHash,
				Model:                started.Model,
				RequestTimestamp:     started.RequestTimestamp,
				PromptTokenCount:     uint64(testData.Rand().IntInRange(1, 512)),
				CompletionTokenCount: uint64(testData.Rand().IntInRange(1, 512)),
			}
			return []simsx.SimAccount{executor}, msg
		}

		// Path 2: fresh finish-first.
		ta, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}
		modelID, ok := PickRandomGovernanceModelID(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}

		sdkCtx := sdk.UnwrapSDKContext(ctx)
		timestamp := sdkCtx.BlockTime().UnixNano()

		executorSigBytes := make([]byte, 64)
		testData.Rand().Read(executorSigBytes)
		responseHashBytes := make([]byte, 32)
		testData.Rand().Read(responseHashBytes)
		promptHashBytes := make([]byte, 32)
		testData.Rand().Read(promptHashBytes)
		originalPromptHashBytes := make([]byte, 32)
		testData.Rand().Read(originalPromptHashBytes)

		msg := &types.MsgFinishInference{
			Creator:              ta.AddressBech32,
			ExecutedBy:           ta.AddressBech32,
			TransferredBy:        ta.AddressBech32,
			RequestedBy:          ta.AddressBech32,
			ExecutorSignature:    base64.StdEncoding.EncodeToString(executorSigBytes),
			ResponseHash:         base64.StdEncoding.EncodeToString(responseHashBytes),
			PromptHash:           base64.StdEncoding.EncodeToString(promptHashBytes),
			OriginalPromptHash:   base64.StdEncoding.EncodeToString(originalPromptHashBytes),
			Model:                modelID,
			RequestTimestamp:     timestamp,
			PromptTokenCount:     uint64(testData.Rand().IntInRange(1, 512)),
			CompletionTokenCount: uint64(testData.Rand().IntInRange(1, 512)),
		}
		devSig, err := SignDevFinish(ta, msg)
		if err != nil {
			reporter.Skipf("SignDevFinish failed: %v", err)
			return nil, nil
		}
		msg.InferenceId = devSig
		taSig, err := SignTAFinish(ta, msg)
		if err != nil {
			reporter.Skipf("SignTAFinish failed: %v", err)
			return nil, nil
		}
		msg.TransferSignature = taSig
		return []simsx.SimAccount{ta}, msg
	}
}

// MsgValidationFactory creates a factory for MsgValidation.
//
// Paired-state rebind. The handler's GetInference branch
// (msg_server_validation.go) returns ErrInferenceNotFound as a
// HARD error; simsx treats hard-error tx as a fatal simulation failure
// and aborts the run. To stay clear of that
// path, this factory picks a random InferenceId from the Inferences
// collection if any exist; if empty (early sim blocks, before Start has
// landed any inference) it Skips the reporter so simsx counts the op as
// «no eligible candidate» rather than failed.
//
// ValueDecimal is generated via shopspring/decimal (no float in op gen
// per plan §145 determinism guards) and is always ABOVE the picked
// model's ValidationThreshold so the handler takes the «passed» branch
// (msg_server_validation.go): inference → VALIDATED, no group
// proposal. The invalidation-voting branch (value ≤ threshold) submits
// a cosmos `group` proposal requiring the validator to be a registered
// group member — that membership path is deferred to Phase 3 (it needs
// the full PoC validator-rotation flow, not a sim-only shortcut).
//
// Exercises: ValidateBasic; CheckPermission (Active ∨ PreviousActive,
// permissions.go); GetParticipant lookup; GetInference (found
// branch); transient validation cache lookup; the passing-validation
// state transition.
func MsgValidationFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgValidation] {
	return func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgValidation) {
		if err := EnsureActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("active-participants bootstrap failed: %v", err)
			return nil, nil
		}
		if err := EnsureComputeValidators(ctx, k); err != nil {
			reporter.Skipf("compute-validators refresh failed: %v", err)
			return nil, nil
		}
		if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
			reporter.Skipf("models epoch-group seeding failed: %v", err)
			return nil, nil
		}
		if err := EnsureMembersInEpochGroup(ctx, k); err != nil {
			reporter.Skipf("members epoch-group seeding failed: %v", err)
			return nil, nil
		}
		inference, ok := PickRandomFinishedInference(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}
		// Validator is drawn from the inference's OWN epoch (not the current
		// epoch): the handler keys the transient validation cache by
		// inference.EpochId, so a current-epoch validator would miss the cache
		// after an epoch transition and the handler would hard-fail with
		// ErrParticipantNotFound. Excludes the executor —
		// msg_server_validation.go hard-rejects self-validation.
		validator, ok := PickRandomActiveSimAccountExcluding(ctx, k, testData, reporter, inference.EpochId, inference.ExecutedBy)
		if !ok {
			return nil, nil
		}

		valueDec := passingValidationValue(ctx, k, testData, inference.Model)

		msg := &types.MsgValidation{
			Creator:      validator.AddressBech32,
			InferenceId:  inference.InferenceId,
			ValueDecimal: types.DecimalFromDecimal(valueDec),
		}
		return []simsx.SimAccount{validator}, msg
	}
}

// MsgClaimRewardsFactory creates a factory for MsgClaimRewards.
//
// Produces a ValidateBasic-passing msg with EpochIndex=1 (must be > 0
// per message_claim_rewards.go) and a random Seed. At sim genesis
// EffectiveEpochIndex=0, so handler's validateRequest hits the
// currentEpoch==0 branch (msg_server_claim_rewards.go) and
// returns a graceful response{Result: "..."} with nil error. Op
// delivered, gas consumed, no state mutation.
//
// Real reward claiming requires SettleAmount records, which are
// produced by upstream PoC/epoch flows or by completed inference
// settlement — both outside this first-wave scope.
//
// Exercises: ValidateBasic; CheckPermission (Active ∨ PreviousActive,
// permissions.go); validateRequest's currentEpoch==0 early-return
// path.
func MsgClaimRewardsFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgClaimRewards] {
	return func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgClaimRewards) {
		if err := EnsureActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("bootstrap failed: %v", err)
			return nil, nil
		}
		if err := EnsureComputeValidators(ctx, k); err != nil {
			reporter.Skipf("compute-validators refresh failed: %v", err)
			return nil, nil
		}
		claimant, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}

		msg := &types.MsgClaimRewards{
			Creator:    claimant.AddressBech32,
			Seed:       testData.Rand().Int63(),
			EpochIndex: 1,
		}
		return []simsx.SimAccount{claimant}, msg
	}
}

// passingValidationValue returns a ValueDecimal strictly above the given
// model's ValidationThreshold, so MsgValidation routes through the
// «passed» branch (msg_server_validation.go). Uses integer-percent
// arithmetic over shopspring/decimal — no float, deterministic.
//
// Falls back to a fixed 0.99 when the model or its threshold can't be
// resolved (the handler would then evaluate against whatever threshold
// it loads; 0.99 clears every realistic mainnet threshold ≤ 0.98).
func passingValidationValue(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
	modelID string,
) decimal.Decimal {
	const maxPct = 100
	minPct := 99
	if model, found := k.GetGovernanceModel(ctx, modelID); found && model.ValidationThreshold != nil {
		// threshold is in [0,1]; smallest integer percentage strictly
		// above it = floor(threshold*100) + 1.
		thresholdPct := model.ValidationThreshold.ToDecimal().
			Mul(decimal.NewFromInt(100)).Floor().IntPart()
		// Assigned unconditionally — int(thresholdPct)+1 IS the smallest
		// integer percent strictly above the threshold. The old «p < minPct»
		// guard left minPct at the 99 default for thresholds ≥ 0.99, letting
		// IntInRange emit a value EQUAL to (not above) the threshold. The
		// minPct>maxPct clamp below covers the degenerate threshold = 1.0 case.
		minPct = int(thresholdPct) + 1
	}
	if minPct > maxPct {
		minPct = maxPct
	}
	numerator := testData.Rand().IntInRange(minPct, maxPct+1)
	return decimal.NewFromInt(int64(numerator)).Div(decimal.NewFromInt(100))
}
