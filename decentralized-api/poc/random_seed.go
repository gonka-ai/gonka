package poc

import (
	"decentralized-api/cosmosclient"
	"encoding/binary"
	"encoding/hex"
	"github.com/productscience/inference/api/inference/inference"
	"log/slog"
	"math/rand"
)

var UpcomingSeed SeedInfo
var CurrentSeed SeedInfo

type SeedInfo struct {
	Seed      int64
	Height    int64
	Signature string
}

func GenerateSeed(blockHeight int64, transactionRecorder *cosmosclient.InferenceCosmosClient) {
	slog.Debug("Old Seed Signature", "seed", CurrentSeed)
	err := getNextSeedSignature(blockHeight, transactionRecorder)
	if err != nil {
		slog.Error("Failed to get next seed signature", "error", err)
		return
	}
	slog.Debug("New Seed Signature", "seed", UpcomingSeed)

	// TODO: submit message
}

// once the new stage has started, request our money!
// if proofofcompute.IsSetNewValidatorsStage(blockHeight)
func RequestMoney(transactionRecorder *cosmosclient.InferenceCosmosClient) {
	defer func() { CurrentSeed = UpcomingSeed }()

	slog.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", "seed", CurrentSeed)
	err := transactionRecorder.ClaimRewards(&inference.MsgClaimRewards{
		Seed:           CurrentSeed.Seed,
		PocStartHeight: uint64(CurrentSeed.Height),
	})
	if err != nil {
		slog.Error("Failed to send ClaimRewards transaction", "error", err)
	}
}

func getNextSeedSignature(blockHeight int64, transactionRecorder *cosmosclient.InferenceCosmosClient) error {
	newSeed := rand.Int63()
	newHeight := blockHeight
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(newSeed))
	signature, err := transactionRecorder.SignBytes(seedBytes)
	if err != nil {
		slog.Error("Failed to sign bytes", "error", err)
		return err
	}
	UpcomingSeed = SeedInfo{
		Seed:      newSeed,
		Height:    newHeight,
		Signature: hex.EncodeToString(signature),
	}
	return nil
}
