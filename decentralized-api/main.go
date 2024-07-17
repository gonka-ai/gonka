package main

import "context"

func main() {
	recorder, err := NewInferenceCosmosClient(context.Background(), "cosmos", "alice")
	if err != nil {
		panic(err)
	}
	StartInferenceServerWrapper(*recorder)
}
