package poc

import (
	"decentralized-api/chainevents"
	"decentralized-api/cosmosclient"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/proofofcompute"
	"log"
	"sync"
)

type PoCOrchestrator struct {
	results    []*ProofOfCompute
	startChan  chan StartPoCEvent
	stopChan   chan StopPoCEvent
	running    bool
	mu         sync.Mutex
	pubKey     string
	difficulty int
}

type StartPoCEvent struct {
	blockHeight int64
	blockHash   string
}

type StopPoCEvent struct {
	action func([]*ProofOfCompute)
}

type ProofOfCompute struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	Nonce       string
	ProofHash   string
}

func NewPoCOrchestrator(pubKey string, difficulty int) *PoCOrchestrator {
	return &PoCOrchestrator{
		results:    []*ProofOfCompute{},
		startChan:  make(chan StartPoCEvent),
		stopChan:   make(chan StopPoCEvent),
		running:    false,
		pubKey:     pubKey,
		difficulty: difficulty,
	}
}

func (o *PoCOrchestrator) acceptHash(hash string) bool {
	return proofofcompute.AcceptHash(hash, o.difficulty)
}

// startProcessing is the function that starts when a start event is triggered
func (o *PoCOrchestrator) startProcessing(event StartPoCEvent) {
	o.mu.Lock()
	o.results = []*ProofOfCompute{}
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

				proof := ProofOfCompute{
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
func (o *PoCOrchestrator) stopProcessing() []*ProofOfCompute {
	// Send the signal to stop the goroutine
	close(o.stopChan)

	o.mu.Lock()
	defer o.mu.Unlock()

	results := o.results

	return results
}

// Run listens for start and stop events
func (o *PoCOrchestrator) Run() {
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
func (o *PoCOrchestrator) isRunning() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running
}

// StartProcessing triggers the start event
func (o *PoCOrchestrator) StartProcessing(event StartPoCEvent) {
	o.mu.Lock()
	o.stopChan = make(chan StopPoCEvent) // Reset stop channel for the next run
	o.mu.Unlock()
	o.startChan <- event
}

// StopProcessing triggers the stop event
func (o *PoCOrchestrator) StopProcessing(action func([]*ProofOfCompute)) {
	o.stopChan <- StopPoCEvent{action: action}
}

func ProcessNewBlockEvent(orchestrator *PoCOrchestrator, event *chainevents.JSONRPCResponse, transactionRecorder cosmosclient.InferenceCosmosClient) {
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

	if proofofcompute.IsStartOfPoCStage(blockHeight) {
		pocEvent := StartPoCEvent{blockHash: blockHash, blockHeight: blockHeight}
		orchestrator.StartProcessing(pocEvent)
		return
	}

	if proofofcompute.IsEndOfPoCStage(blockHeight) {
		orchestrator.StopProcessing(createSubmitPoCCallback(blockHeight, transactionRecorder))
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

func createSubmitPoCCallback(blockHeight int64, transactionRecorder cosmosclient.InferenceCosmosClient) func(proofs []*ProofOfCompute) {
	return func(proofs []*ProofOfCompute) {
		nonce := make([]string, len(proofs))
		for i, p := range proofs {
			nonce[i] = p.Nonce
		}

		message := inference.MsgSubmitPow{
			BlockHeight: blockHeight,
			Nonce:       nonce,
		}

		err := transactionRecorder.SubmitPoC(&message)
		if err != nil {
			log.Printf("Failed to send SubmitPoC transaction. %v", err)
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
