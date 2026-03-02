package bls

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"
	"fmt"
	"sync"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
	"golang.org/x/sync/singleflight"
)

const (
	blsLogTag = "BLS Manager: "
)

// BlsManager handles all BLS operations including DKG dealing, verification, and group key validation
type BlsManager struct {
	cosmosClient     cosmosclient.InferenceCosmosClient
	ctx              context.Context
	cache            *VerificationCache
	recoverySF       singleflight.Group
	maxCacheSize     uint64
	dealerOpeningsMu sync.RWMutex
	dealerOpenings   map[dealerOpeningKey]dealerOpeningRecord
}

// VerificationResult holds the results of DKG verification for an epoch
type VerificationResult struct {
	EpochID           uint64
	DkgPhase          types.DKGPhase // The DKG phase when verification was performed
	IsParticipant     bool
	SlotRange         [2]uint32      // [start_index, end_index]
	DealerShares      [][]fr.Element // dealer_index -> [slot_shares...]
	DealerValidity    []bool         // dealer_index -> validity
	AggregatedShares  []fr.Element   // slot_offset -> aggregated_share
	ValidDealers      []bool         // final consensus validity of each dealer (after majority voting)
	GroupPublicKey    []byte         // the final group public key (when DKG is completed)
	ComplaintEvidence map[uint32]DealerComplaintEvidence
}

type DealerComplaintEvidence struct {
	DisputedSlotIndex       uint32
	DisputedCiphertextIndex uint32
}

type dealerOpeningKey struct {
	epochID         uint64
	recipientIndex  uint32
	ciphertextIndex uint32
}

type dealerOpeningRecord struct {
	slotIndex  uint32
	shareBytes []byte
	seed       []byte
}

// VerificationCache manages verification results for multiple epochs
type VerificationCache struct {
	sync.RWMutex
	results map[uint64]*VerificationResult
}

func NewVerificationCache() *VerificationCache {
	return &VerificationCache{
		results: make(map[uint64]*VerificationResult),
	}
}

func (vc *VerificationCache) Store(result *VerificationResult) {
	if result == nil {
		return
	}

	vc.Lock()
	defer vc.Unlock()

	vc.results[result.EpochID] = result

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

func (vc *VerificationCache) Get(epochID uint64) *VerificationResult {
	vc.RLock()
	defer vc.RUnlock()
	return vc.results[epochID]
}

func (vc *VerificationCache) GetCurrent() *VerificationResult {
	vc.RLock()
	defer vc.RUnlock()

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

func (vc *VerificationCache) GetCachedEpochs() []uint64 {
	vc.RLock()
	defer vc.RUnlock()

	epochs := make([]uint64, 0, len(vc.results))
	for epochID := range vc.results {
		epochs = append(epochs, epochID)
	}
	return epochs
}

// ParticipantInfo represents participant information for DKG
type ParticipantInfo struct {
	Address                    string
	Secp256K1PublicKey         []byte
	AllowedSecp256K1PublicKeys [][]byte
	SlotStartIndex             uint32
	SlotEndIndex               uint32
}

// SlotAssignment represents the slot assignment for a participant
type SlotAssignment struct {
	StartSlot uint32
	EndSlot   uint32
}

// NewBlsManager creates a new unified BLS manager
func NewBlsManager(cosmosClient cosmosclient.InferenceCosmosClient) *BlsManager {
	return &BlsManager{
		cosmosClient:   cosmosClient,
		ctx:            context.Background(), // Use background context for chain queries
		cache:          NewVerificationCache(),
		dealerOpenings: make(map[dealerOpeningKey]dealerOpeningRecord),
	}
}

// ensureConsensusSharesComplete enforces fail-closed signing semantics:
// when consensus ValidDealers is present, every consensus-valid dealer must have
// a share for each local slot, otherwise signing is aborted.
func (bm *BlsManager) ensureConsensusSharesComplete(result *VerificationResult) error {
	if result == nil {
		return fmt.Errorf("verification result is nil")
	}
	if len(result.AggregatedShares) == 0 {
		return fmt.Errorf("no aggregated shares available")
	}

	// Backward-compatible fallback for epochs/results that don't include consensus valid dealers.
	if len(result.ValidDealers) != len(result.DealerShares) {
		return nil
	}

	requiredSlots := len(result.AggregatedShares)
	for dealerIdx, isValid := range result.ValidDealers {
		if !isValid {
			continue
		}
		shares := result.DealerShares[dealerIdx]
		if len(shares) < requiredSlots {
			return fmt.Errorf("missing shares for consensus-valid dealer %d: have %d, need %d", dealerIdx, len(shares), requiredSlots)
		}
	}

	return nil
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

// GetOrRecoverVerificationResult returns cached result or recovers from chain
func (bm *BlsManager) GetOrRecoverVerificationResult(epochID uint64) (*VerificationResult, error) {
	if result := bm.cache.Get(epochID); result != nil {
		return result, nil
	}

	key := fmt.Sprintf("recover-%d", epochID)
	_, err, _ := bm.recoverySF.Do(key, func() (interface{}, error) {
		if result := bm.cache.Get(epochID); result != nil {
			return result, nil
		}

		ctx, cancel := context.WithTimeout(bm.ctx, 60*time.Second)
		defer cancel()

		blsQueryClient := bm.cosmosClient.NewBLSQueryClient()
		res, err := blsQueryClient.EpochBLSData(ctx, &types.QueryEpochBLSDataRequest{
			EpochId: epochID,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to query epoch data: %w", err)
		}

		completed, err := bm.setupAndPerformVerification(epochID, &res.EpochData)
		if err != nil {
			return nil, fmt.Errorf("failed to recover: %w", err)
		}
		if !completed {
			return nil, fmt.Errorf("not a participant in epoch %d", epochID)
		}

		return bm.cache.Get(epochID), nil
	})

	if err != nil {
		return nil, err
	}
	return bm.cache.Get(epochID), nil
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

func (bm *BlsManager) storeDealerOpeningRecord(epochID uint64, recipientIndex, ciphertextIndex, slotIndex uint32, shareBytes, seed []byte) {
	bm.dealerOpeningsMu.Lock()
	defer bm.dealerOpeningsMu.Unlock()

	key := dealerOpeningKey{
		epochID:         epochID,
		recipientIndex:  recipientIndex,
		ciphertextIndex: ciphertextIndex,
	}
	bm.dealerOpenings[key] = dealerOpeningRecord{
		slotIndex:  slotIndex,
		shareBytes: append([]byte(nil), shareBytes...),
		seed:       append([]byte(nil), seed...),
	}
}

func (bm *BlsManager) getDealerOpeningRecord(epochID uint64, recipientIndex, ciphertextIndex uint32) (dealerOpeningRecord, bool) {
	bm.dealerOpeningsMu.RLock()
	defer bm.dealerOpeningsMu.RUnlock()
	key := dealerOpeningKey{
		epochID:         epochID,
		recipientIndex:  recipientIndex,
		ciphertextIndex: ciphertextIndex,
	}
	record, ok := bm.dealerOpenings[key]
	return record, ok
}

func (bm *BlsManager) deleteDealerOpeningsForEpoch(epochID uint64) {
	bm.dealerOpeningsMu.Lock()
	defer bm.dealerOpeningsMu.Unlock()
	for key := range bm.dealerOpenings {
		if key.epochID == epochID {
			delete(bm.dealerOpenings, key)
		}
	}
}
