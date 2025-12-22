//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"

	"decentralized-api/payloadstorage"
)

func main() {
	ctx := context.Background()

	// Verify env vars are set
	if os.Getenv("PGHOST") == "" {
		fmt.Println("PGHOST not set")
		os.Exit(1)
	}

	storage, err := payloadstorage.NewPostgresStorage(ctx)
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	// Store test data
	err = storage.Store(ctx, "manual-test-001", 999, `{"prompt": "hello world"}`, `{"response": "hi there"}`)
	if err != nil {
		fmt.Printf("Failed to store: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Stored successfully")

	// Retrieve
	prompt, response, err := storage.Retrieve(ctx, "manual-test-001", 999)
	if err != nil {
		fmt.Printf("Failed to retrieve: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Retrieved: prompt=%s, response=%s\n", prompt, response)
}
