package main

import (
	"context"
	gonkaopenai "github.com/libermans/gonka-openai/go"
	"github.com/openai/openai-go"
)

var (
	GONKA_PRIVATE_KEY       = "0x1234..." // ECDSA private key for signing requests
	INTERNAL_TEST_NET_ADDR  = "http://34.9.136.116:30000/v1"
	INTERNAL_TEST_NET_MODEL = "Qwen/Qwen2.5-7B-Instruct"
)

func main() {
	// Private key can be provided directly or through environment variable GONKA_PRIVATE_KEY
	client, err := gonkaopenai.NewGonkaOpenAI(gonkaopenai.Options{
		GonkaPrivateKey: GONKA_PRIVATE_KEY,
		Endpoints:       []string{INTERNAL_TEST_NET_ADDR}, // Gonka endpoints
		// Optional parameters:
		// GonkaAddress: "cosmos1...", // Override derived Cosmos address
	})
	if err != nil {
		panic(err)
	}

	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: INTERNAL_TEST_NET_MODEL,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Hello!"),
		},
	})
	if err != nil {
		panic(err)
	}

	println(resp.Choices[0].Message.Content)
}
