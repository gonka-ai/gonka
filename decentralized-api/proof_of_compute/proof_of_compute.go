package proof_of_compute

import (
	"crypto/sha256"
	"decentralized-api/chain_events"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/cometbft/cometbft/crypto"
	"log"
	"sync"
	"time"
)

type POWOrchestrator struct {
	results   []string
	startChan chan StartPowEvent
	stopChan  chan struct{}
	running   bool
	mu        sync.Mutex
}

type StartPowEvent struct {
	blockHeight uint64
	blockHash   string
	pubKey      string
}

func NewPowOrchestrator() *POWOrchestrator {
	return &POWOrchestrator{
		results:   []string{},
		startChan: make(chan StartPowEvent),
		stopChan:  make(chan struct{}),
	}
}

// startProcessing is the function that starts when a start event is triggered
func (o *POWOrchestrator) startProcessing(event StartPowEvent) {
	o.mu.Lock()
	o.running = true
	o.mu.Unlock()

	go func() {
		for {
			select {
			case <-o.stopChan:
				// Stop as soon as the stop signal is received
				return
			default:
				// Execute the function and store the result
				result := o.proofOfCompute(event)
				o.mu.Lock()
				o.results = append(o.results, result)
				o.mu.Unlock()

				// Simulate time-consuming task
				time.Sleep(500 * time.Millisecond)
			}
		}
	}()
}

// proofOfCompute represents the task you want to execute repeatedly
func (o *POWOrchestrator) proofOfCompute(event StartPowEvent) string {
	// Step 1: Concatenate hash and pubKey
	concat := event.blockHash + event.pubKey

	// Step 2: Generate random bit sequence of the same length as concatenated string
	randomBits := generateRandomBytes(len(concat))

	// Step 3: XOR random bit sequence with the concatenated string
	concatBytes := []byte(concat)
	xorResult := xorBytes(concatBytes, randomBits)

	// Step 4: Apply SHA-256 to the XOR result
	hashResult := sha256.Sum256(xorResult)

	// Return the hash result as a hex string
	return hex.EncodeToString(hashResult[:])
}

func xorBytes(a, b []byte) []byte {
	length := len(a)
	if len(b) < length {
		length = len(b)
	}

	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = a[i] ^ b[i]
	}
	return result
}

func generateRandomBytes(length int) []byte {
	randomBytes := crypto.CRandBytes(length)
	return randomBytes
}

// StopProcessing stops the processing and returns the results immediately
func (o *POWOrchestrator) stopProcessing() []string {
	// Send the signal to stop the goroutine
	close(o.stopChan)

	o.mu.Lock()
	defer o.mu.Unlock()

	results := o.results
	o.results = []string{} // Clear the results for the next start event
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
		case <-o.stopChan:
			if o.isRunning() {
				fmt.Println("Stop event received, stopping...")
				results := o.stopProcessing()
				fmt.Println("Final results:", results)
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
	o.stopChan = make(chan struct{}) // Reset stop channel for the next run
	o.mu.Unlock()
	o.startChan <- event
}

// StopProcessing triggers the stop event
func (o *POWOrchestrator) StopProcessing() {
	o.stopChan <- struct{}{}
}

func ProcessNewBlockEvent(orchestrator *POWOrchestrator, event *chain_events.JSONRPCResponse) {
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
		// PRTODO: retrieve pubKey for the current node
		powEvent := StartPowEvent{blockHash: blockHash, blockHeight: blockHeight, pubKey: "pubKey"}
		orchestrator.StartProcessing(powEvent)
		return
	}

	if blockHeight%300 == 0 {
		orchestrator.StopProcessing()
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
