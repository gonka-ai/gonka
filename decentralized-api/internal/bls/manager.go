package bls

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

const (
	blsLogTag = "BLS Manager: "
)

// BlsManager handles all BLS operations including DKG dealing, verification, and group key validation
type BlsManager struct {
	cosmosClient cosmosclient.InferenceCosmosClient
	ctx          context.Context // Context for chain queries

	// Verification state management
	cache *VerificationCache // Cache for multiple epochs

	// Configuration
	maxCacheSize uint64
}

// VerificationResult holds the results of DKG verification for an epoch
type VerificationResult struct {
	EpochID          uint64
	DkgPhase         types.DKGPhase // The DKG phase when verification was performed
	IsParticipant    bool
	SlotRange        [2]uint32      // [start_index, end_index]
	DealerShares     [][]fr.Element // dealer_index -> [slot_shares...]
	DealerValidity   []bool         // dealer_index -> validity
	AggregatedShares []fr.Element   // slot_offset -> aggregated_share
	ValidDealers     []bool         // final consensus validity of each dealer (after majority voting)
	GroupPublicKey   []byte         // the final group public key (when DKG is completed)
}

// VerificationCache manages verification results for multiple epochs
type VerificationCache struct {
	results map[uint64]*VerificationResult // epochID -> VerificationResult
}

// NewVerificationCache creates a new verification cache
func NewVerificationCache() *VerificationCache {
	return &VerificationCache{
		results: make(map[uint64]*VerificationResult),
	}
}

// Store adds a verification result to the cache and cleans up old entries
func (vc *VerificationCache) Store(result *VerificationResult) {
	if result == nil {
		return
	}

	// Store the new result
	vc.results[result.EpochID] = result

	// Clean up old entries (keep only current and previous epoch)
	if result.EpochID >= 2 {
		epochToRemove := result.EpochID - 2
		if _, exists := vc.results[epochToRemove]; exists {
			delete(vc.results, epochToRemove)
			logging.Debug(verifierLogTag+"Removed old verification result from cache", inferenceTypes.BLS,
				"removedEpochID", epochToRemove,
				"currentEpochID", result.EpochID)
		}
	}

	logging.Debug(verifierLogTag+"Stored verification result in cache", inferenceTypes.BLS,
		"epochID", result.EpochID,
		"cachedEpochs", len(vc.results))
}

// Get retrieves a verification result for a specific epoch
func (vc *VerificationCache) Get(epochID uint64) *VerificationResult {
	return vc.results[epochID]
}

// GetCurrent returns the verification result for the highest epoch ID
func (vc *VerificationCache) GetCurrent() *VerificationResult {
	var current *VerificationResult
	var maxEpochID uint64 = 0

	for epochID, result := range vc.results {
		if epochID > maxEpochID {
			maxEpochID = epochID
			current = result
		}
	}

	return current
}

// GetCachedEpochs returns a list of all cached epoch IDs
func (vc *VerificationCache) GetCachedEpochs() []uint64 {
	epochs := make([]uint64, 0, len(vc.results))
	for epochID := range vc.results {
		epochs = append(epochs, epochID)
	}
	return epochs
}

// ParticipantInfo represents participant information for DKG
type ParticipantInfo struct {
	Address            string
	Secp256K1PublicKey []byte
	SlotStartIndex     uint32
	SlotEndIndex       uint32
}

// SlotAssignment represents the slot assignment for a participant
type SlotAssignment struct {
	StartSlot uint32
	EndSlot   uint32
}

// NewBlsManager creates a new unified BLS manager
func NewBlsManager(cosmosClient cosmosclient.InferenceCosmosClient) *BlsManager {
	return &BlsManager{
		cosmosClient: cosmosClient,
		ctx:          context.Background(), // Use background context for chain queries
		cache:        NewVerificationCache(),
	}
}

// GetVerificationResult returns the verification result for a specific epoch
func (v *BlsManager) GetVerificationResult(epochID uint64) *VerificationResult {
	return v.cache.Get(epochID)
}

// GetCurrentVerificationResult returns the current verification result (highest epoch)
func (v *BlsManager) GetCurrentVerificationResult() *VerificationResult {
	return v.cache.GetCurrent()
}

// GetCachedEpochs returns all cached epoch IDs
func (v *BlsManager) GetCachedEpochs() []uint64 {
	return v.cache.GetCachedEpochs()
}

// storeVerificationResult stores a verification result in the cache
// This method can be extended in the future for additional validation or processing
func (bm *BlsManager) storeVerificationResult(result *VerificationResult) {
	if result == nil {
		logging.Warn(verifierLogTag+"Attempted to store nil verification result", inferenceTypes.BLS)
		return
	}

	bm.cache.Store(result)

	logging.Debug(verifierLogTag+"Stored verification result", inferenceTypes.BLS,
		"epochID", result.EpochID,
		"isParticipant", result.IsParticipant,
		"slotRange", result.SlotRange,
		"totalCachedEpochs", len(bm.cache.GetCachedEpochs()))
}

// ProcessGroupPublicKeyGenerated handles the DKG completion event
func (bm *BlsManager) ProcessGroupPublicKeyGenerated(event *chainevents.JSONRPCResponse) error {
	// Process for verification (updating cache with completed result)
	err := bm.ProcessGroupPublicKeyGeneratedToVerify(event)
	if err != nil {
		logging.Warn(blsLogTag+"Failed to process group public key generated for verification", inferenceTypes.BLS, "error", err)
	}

	// Process for group key validation signing
	err = bm.ProcessGroupPublicKeyGeneratedToSign(event)
	if err != nil {
		logging.Warn(blsLogTag+"Failed to process group public key generated for signing", inferenceTypes.BLS, "error", err)
	}

	return nil
}
