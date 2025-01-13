package poc

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"encoding/binary"
	"encoding/hex"
	"github.com/productscience/inference/api/inference/inference"
	"log/slog"
	"math/rand"
)

func GenerateSeed(blockHeight int64, transactionRecorder *cosmosclient.InferenceCosmosClient, manager *apiconfig.ConfigManager) {
	slog.Debug("Old Seed Signature", "seed", manager.GetCurrentSeed())
	newSeed, err := createNewSeed(blockHeight, transactionRecorder)
	if err != nil {
		slog.Error("Failed to get next seed signature", "error", err)
		return
	}
	err = manager.SetUpcomingSeed(*newSeed)
	if err != nil {
		slog.Error("Failed to set upcoming seed", "error", err)
		return
	}
	slog.Debug("New Seed Signature", "seed", manager.GetUpcomingSeed())

	err = transactionRecorder.SubmitSeed(&inference.MsgSubmitSeed{
		BlockHeight: manager.GetUpcomingSeed().Height,
		Signature:   manager.GetUpcomingSeed().Signature,
	})
	if err != nil {
		slog.Error("Failed to send SubmitSeed transaction", "error", err)
	}
}

func ChangeCurrentSeed(manager *apiconfig.ConfigManager) {
	err := manager.SetPreviousSeed(manager.GetCurrentSeed())
	if err != nil {
		slog.Error("Failed to set previous seed", "error", err)
		return
	}
	err = manager.SetCurrentSeed(manager.GetUpcomingSeed())
	if err != nil {
		slog.Error("Failed to set current seed", "error", err)
		return
	}
	err = manager.SetUpcomingSeed(apiconfig.SeedInfo{})
	if err != nil {
		slog.Error("Failed to set upcoming seed", "error", err)
		return
	}
}

func RequestMoney(transactionRecorder *cosmosclient.InferenceCosmosClient, manager *apiconfig.ConfigManager) {

	// FIXME: we can also imagine a scenario where we weren't updating the seed for a few epochs
	//  e.g. generation fails a few times in a row for some reason
	//  Solution: query seed here?
	seed := manager.GetPreviousSeed()

	slog.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", "seed", seed)
	err := transactionRecorder.ClaimRewards(&inference.MsgClaimRewards{
		Seed:           seed.Seed,
		PocStartHeight: uint64(seed.Height),
	})
	if err != nil {
		slog.Error("Failed to send ClaimRewards transaction", "error", err)
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
		slog.Error("Failed to sign bytes", "error", err)
		return nil, err
	}
	seedInfo := apiconfig.SeedInfo{
		Seed:      newSeed,
		Height:    newHeight,
		Signature: hex.EncodeToString(signature),
	}
	return &seedInfo, nil
}
