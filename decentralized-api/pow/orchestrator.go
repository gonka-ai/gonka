package pow

import (
	"decentralized-api/chainevents"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
)

type POWOrchestrator struct {
	results    []*ProofOfWork
	startChan  chan StartPowEvent
	stopChan   chan struct{}
	running    bool
	mu         sync.Mutex
	pubKey     string
	difficulty int
}

type StartPowEvent struct {
	blockHeight uint64
	blockHash   string
}

type ProofOfWork struct {
	BlockHeight uint64
	BlockHash   string
	PubKey      string
	Nonce       string
	ProofHash   string
}

func NewPowOrchestrator(pubKey string, difficulty int) *POWOrchestrator {
	return &POWOrchestrator{
		results:    []*ProofOfWork{},
		startChan:  make(chan StartPowEvent),
		stopChan:   make(chan struct{}),
		running:    false,
		pubKey:     pubKey,
		difficulty: difficulty,
	}
}

func (o *POWOrchestrator) acceptHash(hash string) bool {
	prefix := strings.Repeat("0", o.difficulty)
	return strings.HasPrefix(hash, prefix)
}

// startProcessing is the function that starts when a start event is triggered
func (o *POWOrchestrator) startProcessing(event StartPowEvent) {
	o.mu.Lock()
	o.running = true
	o.mu.Unlock()

	input := []byte(event.blockHash + o.pubKey)
	nonce := make([]byte, len(input))
	go func() {
		for {
			select {
			case <-o.stopChan:
				// Stop as soon as the stop signal is received
				return
			default:
				// Execute the function and store the result
				hashAndNonce := proofOfWork(input, nonce)

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

	go func() {
		o.sendResults(results)
	}()

	o.results = []*ProofOfWork{} // Clear the results for the next start event
	return results
}

func (o *POWOrchestrator) sendResults(results []*ProofOfWork) {
	// PRTODO: implement!
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

func ProcessNewBlockEvent(orchestrator *POWOrchestrator, event *chainevents.JSONRPCResponse) {
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

	if blockHeight%240 == 0 {
		powEvent := StartPowEvent{blockHash: blockHash, blockHeight: blockHeight}
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

	hash, ok := blockID["Hash"].(string)
	if !ok {
		return "", errors.New("failed to access 'Hash' key or it's not a string")
	}

	return hash, nil
}
