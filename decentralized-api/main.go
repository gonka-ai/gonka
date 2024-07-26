package main

import (
	"context"
	"time"
)

func main() {
	recorder, err := NewInferenceCosmosClientWithRetry(context.Background(), "cosmos", "alice", 5, 5*time.Second)
	if err != nil {
		panic(err)
	}
	StartInferenceServerWrapper(*recorder)
}
