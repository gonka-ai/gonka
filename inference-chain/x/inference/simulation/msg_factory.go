package simulation

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/shopspring/decimal"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Message factories for x/inference.
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
			Creator:      from.Address.String(),
			ValidatorKey: SimValidatorKey(from.Address.String()),
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
		// EnsureModelsInEpochGroup runs FIRST so the model sub-groups
		// exist before any participant-gated factory body touches them.
		if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
			reporter.Skipf("models epoch-group seeding failed: %v", err)
			return nil, nil
		}
		if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("sim active-participants seeding failed: %v", err)
			return nil, nil
		}
		ta, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}
		currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
		if err != nil {
			reporter.Skipf("EffectiveEpochIndex get failed: %v", err)
			return nil, nil
		}
		modelID, ok := PickRandomSupportedGovernanceModelID(ctx, k, testData, reporter, currentEpoch, ta.AddressBech32)
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
		// Order — see MsgStartInferenceFactory comment.
		if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
			reporter.Skipf("models epoch-group seeding failed: %v", err)
			return nil, nil
		}
		if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("sim active-participants seeding failed: %v", err)
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
		currentEpoch, err := k.EffectiveEpochIndex.Get(ctx)
		if err != nil {
			reporter.Skipf("EffectiveEpochIndex get failed: %v", err)
			return nil, nil
		}
		modelID, ok := PickRandomSupportedGovernanceModelID(ctx, k, testData, reporter, currentEpoch, ta.AddressBech32)
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
// per the determinism guards). Most validations submit a value ABOVE
// the picked model's ValidationThreshold so the handler takes the
// «passed» branch (msg_server_validation.go): inference → VALIDATED.
// A configurable minority (failingValidationOneInN) submit a value
// ≤ threshold, taking the invalidation-voting branch: inference →
// VOTING plus two cosmos `group` proposals. SubmitProposal requires the
// proposer to be a registered group member; BuildEpochSubstrate
// (substrate.go) seeds that membership via real EpochGroup.AddMember.
//
// Delivery is wrapped with the tolerateDuplicateValidation result handler:
// the random (validator, inference) pick can re-draw a pair that already
// validated this inference, which the handler rejects with
// ErrDuplicateValidation. That is a legitimate negative outcome — a
// participant cannot validate the same inference twice — so it is reported
// as a successful op instead of aborting the run.
//
// Exercises: ValidateBasic; CheckPermission (Active ∨ PreviousActive,
// permissions.go); GetParticipant lookup; GetInference (found
// branch); transient validation cache lookup; both the passing- and
// failing-validation state transitions; the duplicate-validation
// rejection (ErrDuplicateValidation) as a negative path.
func MsgValidationFactory(k keeper.Keeper) *simsx.ResultHandlingSimMsgFactory[*types.MsgValidation] {
	return simsx.NewSimMsgFactoryWithDeliveryResultHandler(
		func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgValidation, simsx.SimDeliveryResultHandler) {
			// Order — see MsgStartInferenceFactory comment.
			if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
				reporter.Skipf("models epoch-group seeding failed: %v", err)
				return nil, nil, nil
			}
			if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
				reporter.Skipf("sim active-participants seeding failed: %v", err)
				return nil, nil, nil
			}
			inference, ok := PickRandomFinishedInference(ctx, k, testData, reporter)
			if !ok {
				return nil, nil, nil
			}
			// Validator is drawn from the inference's OWN epoch (not the current
			// epoch): the handler keys the transient validation cache by
			// inference.EpochId, so a current-epoch validator would miss the cache
			// after an epoch transition and the handler would hard-fail with
			// ErrParticipantNotFound. Excludes the executor —
			// msg_server_validation.go hard-rejects self-validation.
			validator, ok := PickRandomActiveSimAccountExcluding(ctx, k, testData, reporter, inference.EpochId, inference.ExecutedBy)
			if !ok {
				return nil, nil, nil
			}
			// The handler reads the per-model transient cache by
			// (inference.EpochId, inference.Model, msg.Creator). A participant
			// may be in ActiveParticipantsSet but absent from a specific
			// model's sub-group if they did not commit for that model in the
			// preceding PoC window — skip rather than triggering the handler's
			// ErrParticipantNotFound hard-fail (which aborts the sim).
			if _, hasWeight, _ := k.GetCachedEpochDataModelWeight(ctx, inference.EpochId, inference.Model, validator.AddressBech32); !hasWeight {
				reporter.Skipf("validator %s lacks model-weight cache entry for (epoch=%d, model=%s)",
					validator.AddressBech32, inference.EpochId, inference.Model)
				return nil, nil, nil
			}

			// A configurable minority of validations submit a failing value
			// (≤ threshold), driving the handler's invalidation-voting branch:
			// inference → VOTING + two x/group proposals. The rest pass.
			var valueDec decimal.Decimal
			if testData.Rand().IntInRange(0, failingValidationOneInN) == 0 {
				valueDec = failingValidationValue(ctx, k, testData, inference.Model)
			} else {
				valueDec = passingValidationValue(ctx, k, testData, inference.Model)
			}

			msg := &types.MsgValidation{
				Creator:      validator.AddressBech32,
				InferenceId:  inference.InferenceId,
				ValueDecimal: types.DecimalFromDecimal(valueDec),
			}
			return []simsx.SimAccount{validator}, msg, tolerateDuplicateOrDroppedProposer
		},
	)
}

// tolerateDuplicateOrDroppedProposer is the delivery result handler for
// MsgValidationFactory. It tolerates two known-benign delivery failures:
//
//  1. ErrDuplicateValidation — the factory draws a random
//     (validator, inference) pair each invocation; over a long run it
//     eventually re-draws a pair that already validated that inference,
//     which the handler rejects (addInferenceToEpochGroupValidations,
//     msg_server_validation.go). A participant validating the same
//     inference twice is a legitimate negative outcome the chain is meant
//     to reject.
//
//  2. x/group "not in group: %s: unauthorized" — the failing-validation
//     path calls submitValidationProposalsWithPolicy
//     (msg_server_validation.go:280), which sets the revalidation
//     proposal's proposer to inference.ExecutedBy. If the executor was
//     dropped from the active set at the preceding epoch transition
//     (PoC re-election or slashing), x/group.SubmitProposal
//     (cosmos-sdk x/group/keeper/msg_server.go:581) rejects with
//     ErrUnauthorized. In production this is harmless: the tx is rejected,
//     other validators may still try, and module.go:240
//     expireInferenceAndIssueRefund eventually times the inference out and
//     refunds the client. simsx treats any DeliverTx failure as a hard
//     halt; we tolerate this specific case to mirror production liveness.
//
// Any other delivery error is a real failure and is propagated to abort
// the run.
func tolerateDuplicateOrDroppedProposer(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, types.ErrDuplicateValidation) {
		return nil
	}
	if strings.Contains(err.Error(), "not in group:") {
		return nil
	}
	return err
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
		if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("sim active-participants seeding failed: %v", err)
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

// failingValidationOneInN is the reciprocal of the fraction of MsgValidation
// ops that submit a failing value. 4 ⇒ ~25% fail (a minority), enough to
// exercise the invalidation-voting branch + revalidation flow without
// starving the passing path.
const failingValidationOneInN = 4

// failingValidationValue returns a ValueDecimal at or below the given
// model's ValidationThreshold, so MsgValidation routes through the
// invalidation-voting branch (msg_server_validation.go): the handler sets
// the inference to VOTING and submits the two cosmos `group` proposals.
// Integer-percent arithmetic over shopspring/decimal — no float,
// deterministic. The handler compares with GreaterThan, so a value equal
// to the threshold still fails.
//
// Falls back to 0 when the model or its threshold can't be resolved
// (0 is at or below every realistic threshold).
func failingValidationValue(
	ctx context.Context,
	k keeper.Keeper,
	testData *simsx.ChainDataSource,
	modelID string,
) decimal.Decimal {
	maxPct := 0
	if model, found := k.GetGovernanceModel(ctx, modelID); found && model.ValidationThreshold != nil {
		// largest integer percent still ≤ the threshold
		maxPct = int(model.ValidationThreshold.ToDecimal().
			Mul(decimal.NewFromInt(100)).Floor().IntPart())
	}
	if maxPct < 0 {
		maxPct = 0
	}
	numerator := testData.Rand().IntInRange(0, maxPct+1)
	return decimal.NewFromInt(int64(numerator)).Div(decimal.NewFromInt(100))
}

// MsgRevalidationVoteFactory creates a factory for revalidation-vote
// MsgValidation messages (Revalidation:true) — the x/group vote path that
// drives second-wave invalidation/revalidation.
//
// A failing MsgValidation (failingValidationValue) sends an inference to VOTING
// and submits two x/group proposals — Invalidate and ReValidate — behind the
// model's epoch group policy. This factory casts validator votes on them:
// MsgValidation{Revalidation:true} routes to revalidateInferenceVote
// (msg_server_validation.go), which calls group.Vote with Exec_EXEC_TRY on
// both. Once YES weight crosses the group's 50% PercentageDecisionPolicy the
// group module executes the winning proposal internally, dispatching
// MsgInvalidateInference or MsgRevalidateInference with Creator = policy
// address.
//
// There is no standalone MsgInvalidateInference factory: that message's
// Creator must be the group policy address, but DeliverSimsMsg stamps the
// sender's account sequence and the ante SigVerificationDecorator checks it
// against the policy address's sequence -> ErrWrongSequence (chunk P3-1).
// Every tx this factory delivers is a plain MsgValidation (sender == Creator
// == validator); the inner decision messages are dispatched by the group
// module and never reach the ante handler.
//
// Block-time constraint: the sim advances 5000-10000 s per block while the
// validation group's voting window is 4 minutes, so a proposal is votable
// only in the block it was created. The factory votes only on inferences with
// a live proposal window (PickRandomVotingInferenceWithOpenProposal) and fixes
// the vote direction per inference (revalidationVoteIsPass) so every validator
// that votes on a given inference agrees and the YES weight concentrates on
// one proposal. A vote that still lands on a non-votable proposal — the window
// closed, a same-block vote already executed it, or this validator already
// voted — returns a benign x/group error that tolerateGroupVoteOutcome
// classifies as an expected negative outcome rather than a sim failure.
func MsgRevalidationVoteFactory(
	k keeper.Keeper,
	groupKeeper types.GroupMessageKeeper,
) *simsx.ResultHandlingSimMsgFactory[*types.MsgValidation] {
	return simsx.NewSimMsgFactoryWithDeliveryResultHandler(
		func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgValidation, simsx.SimDeliveryResultHandler) {
			// Order — see MsgStartInferenceFactory comment.
			if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
				reporter.Skipf("models epoch-group seeding failed: %v", err)
				return nil, nil, nil
			}
			if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
				reporter.Skipf("sim active-participants seeding failed: %v", err)
				return nil, nil, nil
			}

			inference, ok := PickRandomVotingInferenceWithOpenProposal(ctx, k, groupKeeper)
			if !ok {
				reporter.Skip("no VOTING inference with an open proposal window")
				return nil, nil, nil
			}
			// Any active participant in the inference's epoch is an x/group
			// member (seeded by BuildEpochSubstrate) and is present in the
			// transient validation cache the handler reads. Revalidation is
			// exempt from the self-validation check (msg_server_validation.go),
			// so the executor may vote too — nothing is excluded.
			voter, ok := PickRandomActiveSimAccountExcluding(ctx, k, testData, reporter, inference.EpochId, "")
			if !ok {
				return nil, nil, nil
			}
			// Same model-weight cache requirement as the primary
			// MsgValidationFactory — see comment there.
			if _, hasWeight, _ := k.GetCachedEpochDataModelWeight(ctx, inference.EpochId, inference.Model, voter.AddressBech32); !hasWeight {
				reporter.Skipf("voter %s lacks model-weight cache entry for (epoch=%d, model=%s)",
					voter.AddressBech32, inference.EpochId, inference.Model)
				return nil, nil, nil
			}

			// Fixed per-inference direction: revalidateInferenceVote derives
			// YES-on-Invalidate vs YES-on-ReValidate from `passed` (value >
			// model threshold), and ValueDecimal selects it.
			var valueDec decimal.Decimal
			if revalidationVoteIsPass(inference.InferenceId) {
				valueDec = passingValidationValue(ctx, k, testData, inference.Model)
			} else {
				valueDec = failingValidationValue(ctx, k, testData, inference.Model)
			}

			msg := &types.MsgValidation{
				Creator:      voter.AddressBech32,
				InferenceId:  inference.InferenceId,
				Revalidation: true,
				ValueDecimal: types.DecimalFromDecimal(valueDec),
			}
			return []simsx.SimAccount{voter}, msg, tolerateGroupVoteOutcome
		},
	)
}

// revalidationVoteIsPass fixes the vote direction for an inference: true =>
// vote to revalidate (the inference stays valid), false => vote to invalidate.
// Derived purely from the inference id so every validator voting on the same
// inference agrees without any shared state; ~1 in 3 pass, the rest
// invalidate — the skew keeps MsgInvalidateInference the reliably-exercised
// path for the smoke assertion.
func revalidationVoteIsPass(inferenceID string) bool {
	var sum uint32
	for i := 0; i < len(inferenceID); i++ {
		sum += uint32(inferenceID[i])
	}
	return sum%3 == 0
}

// tolerateGroupVoteOutcome is the delivery result handler for revalidation
// votes. Concurrent revalidation in a chain whose sim block time outpaces the
// 4-minute voting window legitimately produces non-fatal x/group errors: the
// window closed (ErrExpired "voting period has ended already"), a same-block
// vote already decided or executed the proposal ("proposal not open for
// voting"), or this validator already voted ("store vote"). Those are expected
// negative-path outcomes, reported as a successful op. Any other delivery
// error is a real failure and is propagated to abort the run.
func tolerateGroupVoteOutcome(err error) error {
	if err == nil {
		return nil
	}
	errMsg := err.Error()
	for _, benign := range []string{
		"voting period has ended already",
		"proposal not open for voting",
		"store vote",
	} {
		if strings.Contains(errMsg, benign) {
			return nil
		}
	}
	return err
}
