package txs

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"log"
	"testing"
	"time"
)

func TestName(t *testing.T) {
	const accountName = "join1"
	chainNode := apiconfig.ChainNodeConfig{
		Url:            "http://localhost:8101",
		AccountName:    "join1",
		KeyringBackend: "test",
		// KeyringDir:     "/root/.inference",
		KeyringDir: "/home/zb/jobs/productai/code/inference-ignite/local-test-net/prod-local/join1",
		SeedApiUrl: "http://localhost:9000",
	}

	recorder, err := cosmosclient.NewInferenceCosmosClient(
		context.Background(),
		"gonka",
		chainNode,
	)
	if err != nil {
		panic(err)
	}

	transaction := &inference.MsgFinishInference{
		Creator:              accountName,
		InferenceId:          uuid.NewString(),
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           accountName,
	}

	for {
		if err := recorder.FinishInference(transaction); err != nil {
			log.Println(err)
		}
		time.Sleep(2 * time.Second)
	}
}
