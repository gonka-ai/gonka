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
	"strconv"
	"sync"
)

type PoCOrchestrator struct {
	results    *ProofOfComputeResults
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
	action func(results *ProofOfComputeResults)
}

type ProofOfComputeResults struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	Results     []*ProofOfCompute
}

func (r *ProofOfComputeResults) addResult(proof ProofOfCompute) {
	r.Results = append(r.Results, &proof)
}

type ProofOfCompute struct {
	Nonce     string
	ProofHash string
}

func NewPoCOrchestrator(pubKey string, difficulty int) *PoCOrchestrator {
	return &PoCOrchestrator{
		results:    nil,
		startChan:  make(chan StartPoCEvent),
		stopChan:   make(chan StopPoCEvent),
		running:    false,
		pubKey:     pubKey,
		difficulty: difficulty,
	}
}

func (o *PoCOrchestrator) clearResults(blockHeight int64, blockHash string) {
	o.results = &ProofOfComputeResults{
		BlockHeight: blockHeight,
		BlockHash:   blockHash,
		PubKey:      o.pubKey,
		Results:     []*ProofOfCompute{},
	}
}

func (o *PoCOrchestrator) acceptHash(hash string) bool {
	return proofofcompute.AcceptHash(hash, o.difficulty)
}

// startProcessing is the function that starts when a start event is triggered
func (o *PoCOrchestrator) startProcessing(event StartPoCEvent) {
	o.mu.Lock()
	o.clearResults(event.blockHeight, event.blockHash)
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
					Nonce:     hex.EncodeToString(nonce),
					ProofHash: hashAndNonce.Hash,
				}

				incrementBytes(nonce)

				o.mu.Lock()
				o.results.addResult(proof)
				o.mu.Unlock()
			}
		}
	}()
}

// StopProcessing stops the processing and returns the results immediately
func (o *PoCOrchestrator) stopProcessing() *ProofOfComputeResults {
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
func (o *PoCOrchestrator) StopProcessing(action func(*ProofOfComputeResults)) {
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
		log.Printf("IsStartOfPocStagre: sending StartPoCEvent to the PoC orchestrator")
		pocEvent := StartPoCEvent{blockHash: blockHash, blockHeight: blockHeight}
		orchestrator.StartProcessing(pocEvent)
		return
	}

	if proofofcompute.IsEndOfPoCStage(blockHeight) {
		log.Printf("IsEndOfPoCStage: sending StopPoCEvent to the PoC orchestrator")
		orchestrator.StopProcessing(createSubmitPoCCallback(transactionRecorder))
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

	heightString, ok := header["height"].(string)
	if !ok {
		return 0, errors.New("failed to access 'height' key or it's not a string")
	}

	height, err := strconv.ParseInt(heightString, 10, 64)
	if err != nil {
		return 0, errors.New("Failed to convert retrieve height value to int64")
	}

	return height, nil
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

func createSubmitPoCCallback(transactionRecorder cosmosclient.InferenceCosmosClient) func(proofs *ProofOfComputeResults) {
	return func(proofs *ProofOfComputeResults) {
		nonce := make([]string, len(proofs.Results))
		for i, p := range proofs.Results {
			nonce[i] = p.Nonce
		}

		message := inference.MsgSubmitPoC{
			BlockHeight: proofs.BlockHeight,
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
