package proof_of_compute

import (
	"decentralized-api/chain_events"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

type POWOrchestrator struct {
	results   []string
	startChan chan struct{}
	stopChan  chan struct{}
	running   bool
	mu        sync.Mutex
}

func NewPowOrchestrator() *POWOrchestrator {
	return &POWOrchestrator{
		results:   []string{},
		startChan: make(chan struct{}),
		stopChan:  make(chan struct{}),
	}
}

// startProcessing is the function that starts when a start event is triggered
func (o *POWOrchestrator) startProcessing() {
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
				result := o.proofOfCompute()
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
func (o *POWOrchestrator) proofOfCompute() string {
	return fmt.Sprintf("Result at %v", time.Now())
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
		case <-o.startChan:
			if !o.isRunning() {
				fmt.Println("Start event received, processing...")
				o.startProcessing()
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
func (o *POWOrchestrator) StartProcessing() {
	o.mu.Lock()
	o.stopChan = make(chan struct{}) // Reset stop channel for the next run
	o.mu.Unlock()
	o.startChan <- struct{}{}
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
		orchestrator.StartProcessing()
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
