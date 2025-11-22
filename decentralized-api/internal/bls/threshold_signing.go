package bls

import (
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"fmt"
	"math/big"
	"strconv"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

const (
	thresholdSigningLogTag = "Threshold Signing: "
)

// ProcessThresholdSigningRequested handles EventThresholdSigningRequested events
func (bm *BlsManager) ProcessThresholdSigningRequested(event *chainevents.JSONRPCResponse) error {
	logging.Debug(thresholdSigningLogTag+"Processing threshold signing requested event", inferenceTypes.BLS)

	// Extract event data
	requestIdBytes, err := bm.extractEventData(event, "inference.bls.EventThresholdSigningRequested.request_id")
	if err != nil {
		return fmt.Errorf("failed to extract request_id: %w", err)
	}

	epochIdStr, err := bm.extractEventString(event, "inference.bls.EventThresholdSigningRequested.current_epoch_id")
	if err != nil {
		return fmt.Errorf("failed to extract current_epoch_id: %w", err)
	}

	messageHashBytes, err := bm.extractEventData(event, "inference.bls.EventThresholdSigningRequested.message_hash")
	if err != nil {
		return fmt.Errorf("failed to extract message_hash: %w", err)
	}

	deadlineStr, err := bm.extractEventString(event, "inference.bls.EventThresholdSigningRequested.deadline_block_height")
	if err != nil {
		return fmt.Errorf("failed to extract deadline_block_height: %w", err)
	}

	// Parse epoch ID
	epochId, err := strconv.ParseUint(epochIdStr, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse epoch_id: %w", err)
	}

	// Parse deadline
	deadline, err := strconv.ParseInt(deadlineStr, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse deadline: %w", err)
	}

	logging.Info(thresholdSigningLogTag+"Received threshold signing request", inferenceTypes.BLS,
		"request_id", fmt.Sprintf("%x", requestIdBytes),
		"epoch_id", epochId,
		"deadline", deadline)

	// Get verification result for this epoch from cache
	result := bm.cache.Get(epochId)
	if result == nil {
		logging.Warn(thresholdSigningLogTag+"No verification result found for epoch", inferenceTypes.BLS, "epoch_id", epochId)
		return fmt.Errorf("no verification result found for epoch %d", epochId)
	}

	// Check if we are a participant in this epoch
	if !result.IsParticipant {
		logging.Debug(thresholdSigningLogTag+"Not a participant in this epoch, skipping", inferenceTypes.BLS, "epoch_id", epochId)
		return nil
	}

	// Validate message hash length
	if len(messageHashBytes) != 32 {
		return fmt.Errorf("invalid message hash length: expected 32 bytes, got %d", len(messageHashBytes))
	}

	// Compute partial signatures for our slot range
	err = bm.submitPartialSignatures(epochId, requestIdBytes, messageHashBytes, result)
	if err != nil {
		return fmt.Errorf("failed to submit partial signatures: %w", err)
	}

	logging.Info(thresholdSigningLogTag+"Successfully submitted partial signatures", inferenceTypes.BLS,
		"request_id", fmt.Sprintf("%x", requestIdBytes),
		"epoch_id", epochId,
		"slot_range", result.SlotRange)

	return nil
}

// submitPartialSignatures computes and submits partial signatures for our slot range
func (bm *BlsManager) submitPartialSignatures(epochId uint64, requestId []byte, messageHash []byte, result *VerificationResult) error {
	// Generate slot indices for our range
	var slotIndices []uint32
	for slot := result.SlotRange[0]; slot <= result.SlotRange[1]; slot++ {
		slotIndices = append(slotIndices, slot)
	}

	// Compute partial signature for our slots
	partialSignature, err := bm.computePartialSignature(messageHash, result)
	if err != nil {
		return fmt.Errorf("failed to compute partial signature: %w", err)
	}

	// Submit the partial signature via transaction
	err = bm.cosmosClient.SubmitPartialSignature(requestId, slotIndices, partialSignature)
	if err != nil {
		return fmt.Errorf("failed to submit partial signature transaction: %w", err)
	}

	logging.Debug(thresholdSigningLogTag+"Partial signature submitted", inferenceTypes.BLS,
		"epoch_id", epochId,
		"slot_count", len(slotIndices),
		"signature_length", len(partialSignature))

	return nil
}

// computePartialSignature computes per-slot BLS partial signatures for the given message hash.
// Returns a concatenation of 48-byte compressed G1 signatures (one per slot in our assigned range).
func (bm *BlsManager) computePartialSignature(messageHash []byte, result *VerificationResult) ([]byte, error) {
	if len(result.AggregatedShares) == 0 {
		return nil, fmt.Errorf("no aggregated shares available for signing")
	}

	// Hash the message to a G1 point for signing
	messageG1, err := bm.hashToG1(messageHash)
	if err != nil {
		return nil, fmt.Errorf("failed to hash message to G1: %w", err)
	}

	// For each relative slot offset, compute per-slot signature and append 48 bytes
	var concatenated []byte
	for rel := 0; rel < len(result.AggregatedShares); rel++ {
		sk := result.AggregatedShares[rel]
		var sig bls12381.G1Affine
		sig.ScalarMultiplication(&messageG1, sk.BigInt(new(big.Int)))
		sb := sig.Bytes()
		concatenated = append(concatenated, sb[:]...)
	}
	return concatenated, nil
}

// extractEventData extracts byte data from event (base64, hex, or raw string)
func (bm *BlsManager) extractEventData(event *chainevents.JSONRPCResponse, key string) ([]byte, error) {
	values := event.Result.Events[key]
	if len(values) == 0 {
		return nil, fmt.Errorf("key %s not found in event", key)
	}

	// Tendermint may wrap values in quotes. Remove them first.
	unquoted, _ := utils.UnquoteEventValue(values[0])

	// 1) Try base-64
	if data, err := utils.DecodeBase64IfPossible(unquoted); err == nil {
		return data, nil
	}

	// 2) Try hex
	if data, err := utils.DecodeHex(unquoted); err == nil {
		return data, nil
	}

	// 3) Fallback to raw bytes of the string
	return []byte(unquoted), nil
}

// extractEventString extracts string data from event and removes extra JSON quotes if present
func (bm *BlsManager) extractEventString(event *chainevents.JSONRPCResponse, key string) (string, error) {
	values := event.Result.Events[key]
	if len(values) == 0 {
		return "", fmt.Errorf("key %s not found in event", key)
	}

	// Tendermint sometimes stores values as quoted JSON strings (e.g. "\"2\"").
	if unquoted, err := utils.UnquoteEventValue(values[0]); err == nil {
		return unquoted, nil
	}
	return values[0], nil
}
