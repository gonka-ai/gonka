package poc

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"encoding/binary"
	"encoding/hex"
	"math/rand"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

type RandomSeedManager interface {
	GenerateSeed(epochIndex uint64)
	ChangeCurrentSeed()
	RequestMoney()
}

type RandomSeedManagerImpl struct {
	transactionRecorder cosmosclient.CosmosMessageClient
	configManager       *apiconfig.ConfigManager
}

func NewRandomSeedManager(
	transactionRecorder cosmosclient.CosmosMessageClient,
	configManager *apiconfig.ConfigManager,
) *RandomSeedManagerImpl {
	return &RandomSeedManagerImpl{
		transactionRecorder: transactionRecorder,
		configManager:       configManager,
	}
}

func (rsm *RandomSeedManagerImpl) GenerateSeed(epochIndex uint64) {
	logging.Debug("Old Seed Signature", types.Claims, rsm.configManager.GetCurrentSeed())
	newSeed, err := createNewSeed(epochIndex, rsm.transactionRecorder)
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
	configManager := rsm.configManager
	err := configManager.SetPreviousSeed(configManager.GetCurrentSeed())
	if err != nil {
		logging.Error("Failed to set previous seed", types.Claims, "error", err)
		return
	}
	err = configManager.SetCurrentSeed(configManager.GetUpcomingSeed())
	if err != nil {
		logging.Error("Failed to set current seed", types.Claims, "error", err)
		return
	}
	err = configManager.SetUpcomingSeed(apiconfig.SeedInfo{})
	if err != nil {
		logging.Error("Failed to set upcoming seed", types.Claims, "error", err)
		return
	}
}

func (rsm *RandomSeedManagerImpl) RequestMoney() {
	// FIXME: we can also imagine a scenario where we weren't updating the seed for a few epochs
	//  e.g. generation fails a few times in a row for some reason
	//  Solution: query seed here?
	seed := rsm.configManager.GetPreviousSeed()

	logging.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", types.Claims, "seed", seed)
	err := rsm.transactionRecorder.ClaimRewards(&inference.MsgClaimRewards{
		Seed:       seed.Seed,
		EpochIndex: seed.EpochIndex,
	})
	if err != nil {
		logging.Error("Failed to send ClaimRewards transaction", types.Claims, "error", err)
	}
}

func createNewSeed(
	epoch uint64,
	transactionRecorder cosmosclient.CosmosMessageClient,
) (*apiconfig.SeedInfo, error) {
	newSeed := rand.Int63()
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(newSeed))
	signature, err := transactionRecorder.SignBytes(seedBytes)
	if err != nil {
		logging.Error("Failed to sign bytes", types.Claims, "error", err)
		return nil, err
	}
	return &apiconfig.SeedInfo{
		Seed:       newSeed,
		EpochIndex: epoch,
		Signature:  hex.EncodeToString(signature),
	}, nil
}
