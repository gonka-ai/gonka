package poc

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"encoding/binary"
	"encoding/hex"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"math/rand"
)

func generateSeed(blockHeight int64, transactionRecorder *cosmosclient.InferenceCosmosClient, manager *apiconfig.ConfigManager) {
	logging.Debug("Old Seed Signature", types.Claims, manager.GetCurrentSeed())
	newSeed, err := createNewSeed(blockHeight, transactionRecorder)
	if err != nil {
		logging.Error("Failed to get next seed signature", types.Claims, "error", err)
		return
	}
	err = manager.SetUpcomingSeed(*newSeed)
	if err != nil {
		logging.Error("Failed to set upcoming seed", types.Claims, "error", err)
		return
	}
	logging.Debug("New Seed Signature", types.Claims, "seed", manager.GetUpcomingSeed())

	err = transactionRecorder.SubmitSeed(&inference.MsgSubmitSeed{
		BlockHeight: manager.GetUpcomingSeed().Height,
		Signature:   manager.GetUpcomingSeed().Signature,
	})
	if err != nil {
		logging.Error("Failed to send SubmitSeed transaction", types.Claims, "error", err)
	}
}

func changeCurrentSeed(manager *apiconfig.ConfigManager) {
	err := manager.SetPreviousSeed(manager.GetCurrentSeed())
	if err != nil {
		logging.Error("Failed to set previous seed", types.Claims, "error", err)
		return
	}
	err = manager.SetCurrentSeed(manager.GetUpcomingSeed())
	if err != nil {
		logging.Error("Failed to set current seed", types.Claims, "error", err)
		return
	}
	err = manager.SetUpcomingSeed(apiconfig.SeedInfo{})
	if err != nil {
		logging.Error("Failed to set upcoming seed", types.Claims, "error", err)
		return
	}
}

func requestMoney(transactionRecorder *cosmosclient.InferenceCosmosClient, manager *apiconfig.ConfigManager) {
	// FIXME: we can also imagine a scenario where we weren't updating the seed for a few epochs
	//  e.g. generation fails a few times in a row for some reason
	//  Solution: query seed here?
	seed := manager.GetPreviousSeed()

	logging.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", types.Claims, "seed", seed)
	err := transactionRecorder.ClaimRewards(&inference.MsgClaimRewards{
		Seed:           seed.Seed,
		PocStartHeight: uint64(seed.Height),
	})
	if err != nil {
		logging.Error("Failed to send ClaimRewards transaction", types.Claims, "error", err)
	}
}

func createNewSeed(
	blockHeight int64,
	transactionRecorder *cosmosclient.InferenceCosmosClient,
) (*apiconfig.SeedInfo, error) {
	newSeed := rand.Int63()
	newHeight := blockHeight
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(newSeed))
	signature, err := transactionRecorder.SignBytes(seedBytes)
	if err != nil {
		logging.Error("Failed to sign bytes", types.Claims, "error", err)
		return nil, err
	}
	return &apiconfig.SeedInfo{
		Seed:      newSeed,
		Height:    newHeight,
		Signature: hex.EncodeToString(signature),
	}, nil
}
