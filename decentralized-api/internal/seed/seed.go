package seed

import (
	"encoding/binary"
	"encoding/hex"

	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

// RandomSeedManager manages random seeds for rewards/claims.
type RandomSeedManager interface {
	GenerateSeedInfo(epochIndex uint64)
	GetSeedForEpoch(epochIndex uint64) apiconfig.SeedInfo
	CreateNewSeed(epochIndex uint64) (*apiconfig.SeedInfo, error)
	ChangeCurrentSeed()
	RequestMoney(epochIndex uint64)
}

// RandomSeedManagerImpl is the implementation of RandomSeedManager.
type RandomSeedManagerImpl struct {
	transactionRecorder cosmosclient.CosmosMessageClient
	configManager       *apiconfig.ConfigManager
}

// NewRandomSeedManager creates a new random seed manager.
func NewRandomSeedManager(
	transactionRecorder cosmosclient.CosmosMessageClient,
	configManager *apiconfig.ConfigManager,
) *RandomSeedManagerImpl {
	return &RandomSeedManagerImpl{
		transactionRecorder: transactionRecorder,
		configManager:       configManager,
	}
}

func (rsm *RandomSeedManagerImpl) GenerateSeedInfo(epochIndex uint64) {
	logging.Debug("Old Seed Signature", types.Claims, rsm.configManager.GetCurrentSeed())
	newSeed, err := rsm.CreateNewSeed(epochIndex)
	if err != nil {
		logging.Error("Failed to get next seed signature", types.Claims, "error", err)
		return
	}
	err = rsm.configManager.SetUpcomingSeed(*newSeed)
	if err != nil {
		logging.Error("Failed to set upcoming seed", types.Claims, "error", err)
		return
	}
	logging.Debug("New Seed Signature", types.Claims, "seed", rsm.configManager.GetUpcomingSeed())

	err = rsm.transactionRecorder.SubmitSeed(&inference.MsgSubmitSeed{
		EpochIndex: rsm.configManager.GetUpcomingSeed().EpochIndex,
		Signature:  rsm.configManager.GetUpcomingSeed().Signature,
	})
	if err != nil {
		logging.Error("Failed to send SubmitSeed transaction", types.Claims, "error", err)
	}
}

func (rsm *RandomSeedManagerImpl) ChangeCurrentSeed() {
	rsm.configManager.AdvanceCurrentSeed()
}

func (rsm *RandomSeedManagerImpl) GetSeedForEpoch(epochIndex uint64) apiconfig.SeedInfo {
	previousSeed := rsm.configManager.GetPreviousSeed()
	if previousSeed.EpochIndex == epochIndex && previousSeed.Seed != 0 {
		return previousSeed
	}

	seed, err := rsm.CreateNewSeed(epochIndex)
	if err != nil {
		logging.Error("Failed to create new seed", types.Claims, "error", err)
		return apiconfig.SeedInfo{}
	}
	return *seed
}

func (rsm *RandomSeedManagerImpl) RequestMoney(epochIndex uint64) {
	// FIXME: we can also imagine a scenario where we weren't updating the seed for a few epochs
	//  e.g. generation fails a few times in a row for some reason
	//  Solution: query seed here?
	seed := rsm.GetSeedForEpoch(epochIndex)

	// This will only happen in tests, and it starts a long retry process that
	// obscures good failures
	if seed.EpochIndex == 0 {
		return
	}

	logging.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", types.Claims, "seed", seed)
	err := rsm.transactionRecorder.ClaimRewards(&inference.MsgClaimRewards{
		Seed:       seed.Seed,
		EpochIndex: seed.EpochIndex,
	})
	if err != nil {
		logging.Error("Failed to send ClaimRewards transaction", types.Claims, "error", err)
	}
}

func (rsm *RandomSeedManagerImpl) CreateNewSeed(epochIndex uint64) (*apiconfig.SeedInfo, error) {
	newSeed, err := rsm.createSeedForEpoch(epochIndex)
	if err != nil {
		logging.Error("Failed to get seedBytes", types.Claims, "error", err)
		return nil, err
	}

	// Encode seed for signing
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(newSeed))

	signature, err := rsm.transactionRecorder.SignBytes(seedBytes)
	if err != nil {
		logging.Error("Failed to sign bytes", types.Claims, "error", err)
		return nil, err
	}

	return &apiconfig.SeedInfo{
		Seed:       newSeed,
		EpochIndex: epochIndex,
		Signature:  hex.EncodeToString(signature),
	}, nil
}

// CreateSeedForEpoch generates a deterministic seed for a given epoch.
func CreateSeedForEpoch(signer cosmosclient.CosmosMessageClient, epoch uint64) (int64, error) {
	initialSeedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(initialSeedBytes, epoch)

	signed, err := signer.SignBytes(initialSeedBytes)
	if err != nil {
		logging.Error("Failed to sign bytes", types.Claims, "error", err)
		return 0, err
	}

	signed8bytes := signed[:8]
	newSeed := int64(binary.BigEndian.Uint64(signed8bytes[:]) & ((1 << 63) - 1))
	if newSeed == 0 {
		newSeed = 1
	}

	return newSeed, nil
}

func (rsm *RandomSeedManagerImpl) createSeedForEpoch(epoch uint64) (int64, error) {
	return CreateSeedForEpoch(rsm.transactionRecorder, epoch)
}
