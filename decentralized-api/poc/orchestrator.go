package poc

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainevents"
	"decentralized-api/cosmosclient"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/proofofcompute"
	"github.com/sagikazarmark/slog-shim"
	"log"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
)

type PoCOrchestrator struct {
	results       *ProofOfComputeResults
	startChan     chan StartPoCEvent
	stopChan      chan StopPoCEvent
	mu            sync.Mutex
	wg            sync.WaitGroup
	pubKey        string
	difficulty    int
	runningAtomic atomic.Bool
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
	orchestrator := &PoCOrchestrator{
		results:       nil,
		startChan:     make(chan StartPoCEvent),
		stopChan:      make(chan StopPoCEvent),
		pubKey:        pubKey,
		difficulty:    difficulty,
		runningAtomic: atomic.Bool{},
	}
	orchestrator.runningAtomic.Store(false)
	return orchestrator
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
	o.runningAtomic.Store(true)
	o.mu.Unlock()

	go func() {
		input := proofofcompute.GetInput(event.blockHash, o.pubKey)
		nonce := make([]byte, len(input))
		for {
			if !o.isRunning() {
				return
			}

			// Execute the function and store the result
			hashAndNonce := proofofcompute.ProofOfCompute(input, nonce)

			if o.acceptHash(hashAndNonce.Hash) {
				// Make it trace level maybe or even lower?
				// log.Printf("Hash accepted, adding. input = %s. nonce = %v. hash = %s", hex.EncodeToString(input), hex.EncodeToString(nonce), hashAndNonce.Hash)
				proof := ProofOfCompute{
					Nonce:     hex.EncodeToString(nonce),
					ProofHash: hashAndNonce.Hash,
				}

				o.mu.Lock()
				o.results.addResult(proof)
				o.mu.Unlock()
			}

			incrementBytes(nonce)
		}
	}()
}

// StopProcessing stops the processing and returns the results immediately
func (o *PoCOrchestrator) stopProcessing() *ProofOfComputeResults {
	o.mu.Lock()
	defer o.mu.Unlock()

	results := o.results
	o.runningAtomic.Store(false)

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
				fmt.Println("Final PoC results size:", len(results.Results))
				event.action(results)
			}
		}
	}
}

// isRunning checks if the component is running
func (o *PoCOrchestrator) isRunning() bool {
	return o.runningAtomic.Load()
}

// StartProcessing triggers the start event
func (o *PoCOrchestrator) StartProcessing(event StartPoCEvent) {
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

	// Check for any upcoming upgrade plan
	plan, err := transactionRecorder.GetUpgradePlan()
	if err != nil {
		slog.Error("Unable to get upgrade plan", "error", err)
	} else {
		slog.Info("Upgrade plan", "plan", plan.Plan)
	}

	//for key := range event.Result.Events {
	//	for i, attr := range event.Result.Events[key] {
	//		slog.Debug("\t NewBlockEventValue", "key", key, "attr", attr, "index", i)
	//	}
	//}

	data := event.Result.Data.Value

	blockHeight, err := getBlockHeight(data)
	if err != nil {
		slog.Error("Failed to get blockHeight from event data", "error", err)
		return
	}
	err = apiconfig.SetHeight(blockHeight)
	if err != nil {
		slog.Warn("Failed to write config", "error", err)
	}

	blockHash, err := getBlockHash(data)
	if err != nil {
		slog.Error("Failed to get blockHash from event data", "error", err)
		return
	}

	slog.Debug("New block event received", "blockHeight", blockHeight, "blockHash", blockHash)

	if proofofcompute.IsStartOfPoCStage(blockHeight) {
		slog.Info("IsStartOfPoCStage: sending StartPoCEvent to the PoC orchestrator")
		pocEvent := StartPoCEvent{blockHash: blockHash, blockHeight: blockHeight}
		orchestrator.StartProcessing(pocEvent)
		return
	}

	if proofofcompute.IsEndOfPoCStage(blockHeight) {
		slog.Info("IsEndOfPoCStage: sending StopPoCEvent to the PoC orchestrator")
		orchestrator.StopProcessing(createSubmitPoCCallback(transactionRecorder))
		return
	}
	// once the new stage has started, request our money!
	if proofofcompute.IsSetNewValidatorsStage(blockHeight) {
		go func() {
			err := apiconfig.SetPreviousSeed(apiconfig.GetCurrentSeed())
			if err != nil {
				slog.Error("Failed to set previous seed", "error", err)
				return
			}
			err = apiconfig.SetCurrentSeed(apiconfig.GetUpcomingSeed())
			if err != nil {
				slog.Error("Failed to set current seed", "error", err)
				return
			}
			previousSeed := apiconfig.GetPreviousSeed()
			slog.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", "seed", previousSeed)
			err = transactionRecorder.ClaimRewards(&inference.MsgClaimRewards{
				Seed:           previousSeed.Seed,
				PocStartHeight: uint64(previousSeed.Height),
			})
			if err != nil {
				slog.Error("Failed to send ClaimRewards transaction", "error", err)
			}
		}()
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

		slog.Debug("Old Seed Signature", "seed", apiconfig.GetCurrentSeed())
		err := getNextSeedSignature(proofs, transactionRecorder)
		if err != nil {
			slog.Error("Failed to get next seed signature", "error", err)
			return
		}
		slog.Debug("New Seed Signature", "seed", apiconfig.GetUpcomingSeed())

		message := inference.MsgSubmitPoC{
			BlockHeight:   proofs.BlockHeight,
			Nonce:         nonce,
			SeedSignature: apiconfig.GetUpcomingSeed().Signature,
		}

		log.Printf("Submitting PoC transaction. BlockHeight = %d. len(Nonce) = %d", message.BlockHeight, len(message.Nonce))

		err = transactionRecorder.SubmitPoC(&message)
		if err != nil {
			log.Printf("Failed to send SubmitPoC transaction. %v", err)
		}
	}
}

func getNextSeedSignature(proofs *ProofOfComputeResults, transactionRecorder cosmosclient.InferenceCosmosClient) error {
	newSeed := rand.Int63()
	newHeight := proofs.BlockHeight
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(newSeed))
	signature, err := transactionRecorder.SignBytes(seedBytes)
	if err != nil {
		slog.Error("Failed to sign bytes", "error", err)
		return err
	}
	err = apiconfig.SetUpcomingSeed(apiconfig.SeedInfo{
		Seed:      newSeed,
		Height:    newHeight,
		Signature: hex.EncodeToString(signature),
	})
	if err != nil {
		slog.Error("Failed to set upcoming seed", "error", err)
		return err
	}
	return nil
}

func incrementBytes(nonce []byte) {
	for i := len(nonce) - 1; i >= 0; i-- {
		nonce[i]++
		if nonce[i] != 0 {
			break // If no carry, we're done
		}
	}
}
