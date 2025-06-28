package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	gonkaopenai "github.com/libermans/gonka-openai/go"
	"github.com/openai/openai-go"
)

var (
	ACCOUNT_NAME            = "test-account"
	INTERNAL_TEST_NET_ADDR  = "http://34.9.136.116:30000/v1"
	INTERNAL_TEST_NET_MODEL = "Qwen/Qwen2.5-7B-Instruct"
	INFERENCED_BINARY       = "/Users/dima/cosmos/bin/inferenced"
)

// getPrivateKey exports the private key using inferenced command
func getPrivateKey() (string, error) {
	cmd := exec.Command(INFERENCED_BINARY, "keys", "export", ACCOUNT_NAME, "--unarmored-hex", "--unsafe")
	cmd.Stdin = strings.NewReader("y\n") // Auto-confirm the export

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to export private key: %w", err)
	}

	// Trim whitespace and newlines from the output
	privateKey := strings.TrimSpace(string(output))
	return privateKey, nil
}

func main() {
	// Get private key via inferenced export
	privateKey, err := getPrivateKey()
	if err != nil {
		panic(fmt.Sprintf("Failed to get private key: %v", err))
	}

	fmt.Printf("Successfully exported private key for account: %s\n", ACCOUNT_NAME)

	// Create Gonka OpenAI client
	client, err := gonkaopenai.NewGonkaOpenAI(gonkaopenai.Options{
		GonkaPrivateKey: privateKey,
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
