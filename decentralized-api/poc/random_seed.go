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

func (s *SeedInfo) IsEmpty() bool {
	return s.Seed == 0 && s.Height == 0 && s.Signature == ""
}

func GenerateSeed(blockHeight int64, transactionRecorder *cosmosclient.InferenceCosmosClient) {
	slog.Debug("Old Seed Signature", "seed", CurrentSeed)
	seedInfo, err := getNextSeedSignature(blockHeight, transactionRecorder)
	if err != nil {
		slog.Error("Failed to get next seed signature", "error", err)
		return
	}
	slog.Debug("New Seed Signature", "seed", UpcomingSeed)

	err = transactionRecorder.SubmitSeed(&inference.MsgSubmitSeed{
		BlockHeight: seedInfo.Height,
		Signature:   seedInfo.Signature,
	})
	if err != nil {
		slog.Error("Failed to send SubmitSeed transaction", "error", err)
	}

	UpcomingSeed = *seedInfo
}

func RequestMoney(transactionRecorder *cosmosclient.InferenceCosmosClient) {
	defer func() { CurrentSeed = UpcomingSeed }()

	// FIXME: we can also imagine a scenario where we weren't updating the seed for a few epochs
	//  e.g. generation fails a few times in a row for some reason
	//  Solution: query seed here?
	if CurrentSeed.IsEmpty() {
		slog.Info("IsSetNewValidatorsStage: CurrentSeed is empty, skipping ClaimRewards")
		return
	}

	slog.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", "seed", CurrentSeed)
	err := transactionRecorder.ClaimRewards(&inference.MsgClaimRewards{
		Seed:           CurrentSeed.Seed,
		PocStartHeight: uint64(CurrentSeed.Height),
	})
	if err != nil {
		slog.Error("Failed to send ClaimRewards transaction", "error", err)
	}
}

func getNextSeedSignature(blockHeight int64, transactionRecorder *cosmosclient.InferenceCosmosClient) (*SeedInfo, error) {
	newSeed := rand.Int63()
	newHeight := blockHeight
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(newSeed))
	signature, err := transactionRecorder.SignBytes(seedBytes)
	if err != nil {
		slog.Error("Failed to sign bytes", "error", err)
		return nil, err
	}
	seedInfo := SeedInfo{
		Seed:      newSeed,
		Height:    newHeight,
		Signature: hex.EncodeToString(signature),
	}
	return &seedInfo, nil
}
