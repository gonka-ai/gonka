package proof_of_compute

import (
	"decentralized-api/chain_events"
	"errors"
	"log"
)

func ProcessNewBlockEvent(event *chain_events.JSONRPCResponse) {
	if event.Result.Data.Type != "tendermint/event/NewBlock" {
		log.Fatalf("Expected tendermint/event/NewBlock event, got %s", event.Result.Data.Type)
		return
	}

	data := event.Result.Data.Value

	blockHeight, err := getBlockHeight(data)
	if err != nil {
		log.Printf("Failed to get blockHeight from event data. %v", err)
		return
	}

	blockHash, err := getBlockHash(data)
	if err != nil {
		log.Printf("Failed to get blockHash from event data. %v", err)
		return
	}

	log.Printf("New block event received. blockHeight = %d, blockHash = %s", blockHeight, blockHash)

	if blockHeight%240 == 0 {
		// Start POW
		return
	}

	if blockHeight%300 == 0 {
		// Stop POW
		return
	}
}

func getBlockHeight(data map[string]interface{}) (uint64, error) {
	block, ok := data["block"].(map[string]interface{})
	if !ok {
		return 0, errors.New("failed to access 'block' key")
	}

	header, ok := block["header"].(map[string]interface{})
	if !ok {
		return 0, errors.New("failed to access 'header' key")
	}

	h, ok := header["height"].(float64)
	if !ok {
		return 0, errors.New("failed to access 'height' key or it's not a float64")
	}

	return uint64(h), nil
}

func getBlockHash(data map[string]interface{}) (string, error) {
	blockID, ok := data["block_id"].(map[string]interface{})
	if !ok {
		return "", errors.New("failed to access 'block_id' key")
	}

	hash, ok := blockID["hash"].(string)
	if !ok {
		return "", errors.New("failed to access 'hash' key or it's not a string")
	}

	return hash, nil
}
