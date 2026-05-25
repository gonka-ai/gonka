package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

const (
	EventTypeTeeAttestationSubmitted = "tee_attestation_submitted"
	EventTypeTeeAttestationRevoked   = "tee_attestation_revoked"

	AttributeKeyTeeParticipant     = "participant"
	AttributeKeyTeeNodeID          = "node_id"
	AttributeKeyTeeProvider        = "provider"
	AttributeKeyTeeMeasurement     = "measurement"
	AttributeKeyTeeExpiresAtHeight = "expires_at_height"
	AttributeKeyTeePocStageHeight  = "poc_stage_start_block_height"
	AttributeKeyTeeEnvelopeVersion = "envelope_version"
	AttributeKeyTeeRevocationCount = "revocation_count"
)

func publicKeyLengthForEnvelope(envelopeVersion string) (int, error) {
	switch envelopeVersion {
	case types.TeeEnvelopeVersionV1:
		return 32, nil
	default:
		return 0, fmt.Errorf("unsupported envelope version %q", envelopeVersion)
	}
}

func (k msgServer) SubmitTeeAttestation(goCtx context.Context, msg *types.MsgSubmitTeeAttestation) (*types.MsgSubmitTeeAttestationResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}
	teeParams := params.TeeAttestationParams
	if teeParams == nil || len(teeParams.AllowedProviders) == 0 || len(teeParams.AllowedMeasurements) == 0 {
		return nil, sdkerrors.Wrap(types.ErrTeeAttestationDisabled, "tee attestation not configured: allowed_providers and allowed_measurements must both be non-empty")
	}

	if len(msg.Attestations) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "attestations must not be empty")
	}
	maxEntries := int(teeParams.MaxEntriesPerMessage)
	if maxEntries <= 0 {
		maxEntries = int(types.DefaultMaxEntriesPerTeeAttestationMessage)
	}
	if len(msg.Attestations) > maxEntries {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("too many attestation entries: got %d, max %d", len(msg.Attestations), maxEntries))
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	epochParams := params.EpochParams
	upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "failed to get upcoming epoch")
	}
	epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

	if !epochContext.IsStartOfPocStage(startBlockHeight) {
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
			fmt.Sprintf("start block height %d doesn't match PoC stage start %d",
				startBlockHeight, epochContext.PocStartBlockHeight))
	}
	if !epochContext.IsPoCExchangeWindow(currentBlockHeight) {
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, "PoC exchange window closed")
	}

	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	ttlEpochs := teeParams.AttestationTtlEpochs
	if ttlEpochs == 0 {
		ttlEpochs = 1
	}
	expiresAtHeight := startBlockHeight + int64(ttlEpochs)*epochParams.EpochLength

	seenNodeIDs := make(map[string]struct{}, len(msg.Attestations))
	for _, entry := range msg.Attestations {
		validated, err := validateTeeAttestationEntry(entry, teeParams)
		if err != nil {
			return nil, err
		}
		if _, dup := seenNodeIDs[entry.NodeId]; dup {
			return nil, sdkerrors.Wrap(types.ErrDuplicateNodeId,
				fmt.Sprintf("node_id %q appears more than once in message", entry.NodeId))
		}
		seenNodeIDs[entry.NodeId] = struct{}{}

		previous, found, err := k.GetTeeAttestation(ctx, creatorAddr, entry.NodeId)
		if err != nil {
			return nil, err
		}

		attestation := types.TeeAttestation{
			ParticipantAddress: msg.Creator,
			NodeId:             entry.NodeId,
			PublicKey:          entry.PublicKey,
			Provider:           validated.provider,
			Measurement:        validated.measurement,
			Quote:              entry.Quote,
			QuoteHash:          entry.QuoteHash,
			AttestedAtHeight:   startBlockHeight,
			ExpiresAtHeight:    expiresAtHeight,
			EnvelopeVersion:    validated.envelopeVersion,
		}
		if found && isSameTeeIdentity(previous, attestation) {
			attestation.VerificationStatus = previous.VerificationStatus
			attestation.VerifiedAtHeight = previous.VerifiedAtHeight
			attestation.OkVotingPowerBps = previous.OkVotingPowerBps
			attestation.RejectVotingPowerBps = previous.RejectVotingPowerBps
			attestation.PendingEpochs = previous.PendingEpochs
		}

		if err := k.SetTeeAttestation(ctx, attestation); err != nil {
			return nil, err
		}

		ctx.EventManager().EmitEvent(sdk.NewEvent(
			EventTypeTeeAttestationSubmitted,
			sdk.NewAttribute(AttributeKeyTeeParticipant, msg.Creator),
			sdk.NewAttribute(AttributeKeyTeeNodeID, entry.NodeId),
			sdk.NewAttribute(AttributeKeyTeeProvider, validated.provider),
			sdk.NewAttribute(AttributeKeyTeeEnvelopeVersion, validated.envelopeVersion),
			sdk.NewAttribute(AttributeKeyTeeMeasurement, validated.measurement),
			sdk.NewAttribute(AttributeKeyTeePocStageHeight, fmt.Sprint(startBlockHeight)),
			sdk.NewAttribute(AttributeKeyTeeExpiresAtHeight, fmt.Sprint(expiresAtHeight)),
		))

		k.LogInfo("[TeeAttestation] Stored",
			types.PoC,
			"participant", msg.Creator,
			"node_id", entry.NodeId,
			"provider", validated.provider,
			"poc_stage_start", startBlockHeight,
			"expires_at_height", expiresAtHeight,
		)
	}

	return &types.MsgSubmitTeeAttestationResponse{}, nil
}

type validatedTeeAttestationEntry struct {
	envelopeVersion string
	provider        string
	measurement     string
}

func validateTeeAttestationEntry(entry *types.TeeAttestationEntry, params *types.TeeAttestationParams) (validatedTeeAttestationEntry, error) {
	if entry == nil {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(types.ErrIllegalState, "attestation entry is nil")
	}
	if entry.NodeId == "" {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(types.ErrTeeAttestationNodeIdEmpty, "node_id is empty")
	}
	envelopeVersion := entry.EnvelopeVersion
	if envelopeVersion == "" {
		envelopeVersion = types.TeeEnvelopeVersionV1
	}
	allowedEnvelopes := params.AllowedEnvelopeVersions
	if len(allowedEnvelopes) == 0 {
		allowedEnvelopes = []string{types.TeeEnvelopeVersionV1}
	}
	if !slices.Contains(allowedEnvelopes, envelopeVersion) {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(types.ErrTeeAttestationEnvelopeNotAllowed, envelopeVersion)
	}
	expectedPKLen, err := publicKeyLengthForEnvelope(envelopeVersion)
	if err != nil {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(types.ErrTeeAttestationEnvelopeNotAllowed, err.Error())
	}
	if len(entry.PublicKey) != expectedPKLen {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(
			types.ErrTeeAttestationInvalidPublicKey,
			fmt.Sprintf("envelope %s expects %d bytes, got %d", envelopeVersion, expectedPKLen, len(entry.PublicKey)),
		)
	}
	provider := normalizeTeeProvider(entry.Provider)
	if !isAllowedTeeProvider(params.AllowedProviders, provider) {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(types.ErrTeeAttestationProviderNotAllowed, provider)
	}
	if len(entry.Quote) == 0 {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(
			types.ErrTeeAttestationQuoteRequired,
			fmt.Sprintf("node %q: quote is required", entry.NodeId),
		)
	}
	if len(entry.QuoteHash) != sha256.Size {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(
			types.ErrTeeAttestationQuoteRequired,
			fmt.Sprintf("node %q: quote_hash must be %d bytes sha256, got %d", entry.NodeId, sha256.Size, len(entry.QuoteHash)),
		)
	}
	computedQuoteHash := sha256.Sum256(entry.Quote)
	if !bytes.Equal(entry.QuoteHash, computedQuoteHash[:]) {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(
			types.ErrTeeAttestationQuoteRequired,
			fmt.Sprintf("node %q: quote_hash must equal sha256(quote)", entry.NodeId),
		)
	}

	if entry.Measurement == "" {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(
			types.ErrTeeAttestationMeasurementNotAllowed,
			fmt.Sprintf("node %q: measurement is required", entry.NodeId),
		)
	}
	measurement, err := types.CanonicalMeasurement(entry.Measurement)
	if err != nil {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(types.ErrTeeAttestationMeasurementNotAllowed, err.Error())
	}
	if !slices.Contains(params.AllowedMeasurements, measurement) {
		return validatedTeeAttestationEntry{}, sdkerrors.Wrap(types.ErrTeeAttestationMeasurementNotAllowed, measurement)
	}

	return validatedTeeAttestationEntry{
		envelopeVersion: envelopeVersion,
		provider:        provider,
		measurement:     measurement,
	}, nil
}

func isSameTeeIdentity(previous types.TeeAttestation, next types.TeeAttestation) bool {
	return bytes.Equal(previous.PublicKey, next.PublicKey) &&
		previous.Provider == next.Provider &&
		previous.EnvelopeVersion == next.EnvelopeVersion &&
		previous.Measurement == next.Measurement &&
		bytes.Equal(previous.QuoteHash, next.QuoteHash)
}

func normalizeTeeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func isAllowedTeeProvider(allowed []string, provider string) bool {
	for _, p := range allowed {
		if normalizeTeeProvider(p) == provider {
			return true
		}
	}
	return false
}
