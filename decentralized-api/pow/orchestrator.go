package pow

import (
	"decentralized-api/chainevents"
	"decentralized-api/cosmosclient"
	"encoding/hex"
	"errors"
	"fmt"
	"inference/api/inference/inference"
	"inference/x/inference/proofofcompute"
	"log"
	"sync"
)

type POWOrchestrator struct {
	results    []*ProofOfWork
	startChan  chan StartPowEvent
	stopChan   chan StopPowEvent
	running    bool
	mu         sync.Mutex
	pubKey     string
	difficulty int
}

type StartPowEvent struct {
	blockHeight int64
	blockHash   string
}

type StopPowEvent struct {
	action func([]*ProofOfWork)
}

type ProofOfWork struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	Nonce       string
	ProofHash   string
}

func NewPowOrchestrator(pubKey string, difficulty int) *POWOrchestrator {
	return &POWOrchestrator{
		results:    []*ProofOfWork{},
		startChan:  make(chan StartPowEvent),
		stopChan:   make(chan StopPowEvent),
		running:    false,
		pubKey:     pubKey,
		difficulty: difficulty,
	}
}

func (o *POWOrchestrator) acceptHash(hash string) bool {
	return proofofcompute.AcceptHash(hash, o.difficulty)
}

// startProcessing is the function that starts when a start event is triggered
func (o *POWOrchestrator) startProcessing(event StartPowEvent) {
	o.mu.Lock()
	o.results = []*ProofOfWork{}
	o.running = true
	o.mu.Unlock()

	input := proofofcompute.GetInput(event.blockHash, o.pubKey)
	nonce := make([]byte, len(input))
	go func() {
		for {
			select {
			case <-o.stopChan:
				// Stop as soon as the stop signal is received
				return
			default:
				// Execute the function and store the result
				hashAndNonce := proofofcompute.ProofOfCompute(input, nonce)

				if !o.acceptHash(hashAndNonce.Hash) {
					continue
				}

				proof := ProofOfWork{
					BlockHeight: event.blockHeight,
					BlockHash:   event.blockHash,
					PubKey:      o.pubKey,
					Nonce:       hex.EncodeToString(nonce),
					ProofHash:   hashAndNonce.Hash,
				}

				incrementBytes(nonce)

				o.mu.Lock()
				o.results = append(o.results, &proof)
				o.mu.Unlock()
			}
		}
	}()
}

// StopProcessing stops the processing and returns the results immediately
func (o *POWOrchestrator) stopProcessing() []*ProofOfWork {
	// Send the signal to stop the goroutine
	close(o.stopChan)

	o.mu.Lock()
	defer o.mu.Unlock()

	results := o.results

	return results
}

// Run listens for start and stop events
func (o *POWOrchestrator) Run() {
	for {
		select {
		case event := <-o.startChan:
			if !o.isRunning() {
				fmt.Println("Start event received, processing...")
				o.startProcessing(event)
			}
		case event := <-o.stopChan:
			if o.isRunning() {
				fmt.Println("Stop event received, stopping...")
				results := o.stopProcessing()
				fmt.Println("Final results:", results)
				event.action(results)
			}
		}
	}
}

// isRunning checks if the component is running
func (o *POWOrchestrator) isRunning() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running
}

// StartProcessing triggers the start event
func (o *POWOrchestrator) StartProcessing(event StartPowEvent) {
	o.mu.Lock()
	o.stopChan = make(chan StopPowEvent) // Reset stop channel for the next run
	o.mu.Unlock()
	o.startChan <- event
}

// StopProcessing triggers the stop event
func (o *POWOrchestrator) StopProcessing(action func([]*ProofOfWork)) {
	o.stopChan <- StopPowEvent{action: action}
}

func ProcessNewBlockEvent(orchestrator *POWOrchestrator, event *chainevents.JSONRPCResponse, transactionRecorder cosmosclient.InferenceCosmosClient) {
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
		log.Printf("Failed to get BlockHash from event data. %v", err)
		return
	}

	log.Printf("New block event received. blockHeight = %d, BlockHash = %s", blockHeight, blockHash)

	if proofofcompute.IsStartOfPocStage(blockHeight) {
		powEvent := StartPowEvent{blockHash: blockHash, blockHeight: blockHeight}
		orchestrator.StartProcessing(powEvent)
		return
	}

	if proofofcompute.IsEndOfPocStage(blockHeight) {
		orchestrator.StopProcessing(createSubmitPowCallback(blockHeight, transactionRecorder))
		return
	}
}

func getBlockHeight(data map[string]interface{}) (int64, error) {
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

	return int64(h), nil
}

func getBlockHash(data map[string]interface{}) (string, error) {
	blockID, ok := data["block_id"].(map[string]interface{})
	if !ok {
		return "", errors.New("failed to access 'block_id' key")
	}

	hash, ok := blockID["Hash"].(string)
	if !ok {
		return "", errors.New("failed to access 'Hash' key or it's not a string")
	}

	return hash, nil
}

func createSubmitPowCallback(blockHeight int64, transactionRecorder cosmosclient.InferenceCosmosClient) func(proofs []*ProofOfWork) {
	return func(proofs []*ProofOfWork) {
		nonce := make([]string, len(proofs))
		for i, p := range proofs {
			nonce[i] = p.Nonce
		}

		message := inference.MsgSubmitPow{
			BlockHeight: blockHeight,
			Nonce:       nonce,
		}

		err := transactionRecorder.SubmitPow(&message)
		if err != nil {
			log.Printf("Failed to send SubmitPow transaction. %v", err)
		}
	}
}

func incrementBytes(nonce []byte) {
	for i := len(nonce) - 1; i >= 0; i-- {
		nonce[i]++
		if nonce[i] != 0 {
			break // If no carry, we're done
		}
	}
}
