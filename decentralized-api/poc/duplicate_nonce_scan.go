package poc

import (
	"context"
	crand "crypto/rand"
	"math/big"
	"sync"
	"time"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	MaxClaimedNoncesPerCommit = 1_000_000
	duplicateScanSampleSize   = 5_000
	duplicateScanChunkSize    = 500
	duplicateScanURLCooldown  = 5 * time.Second
	duplicateScanWorkers      = 2
	duplicateScanPollInterval = 100 * time.Millisecond
)

type pocValidationKey struct {
	pocHeight   int64
	participant string
	modelID     string
}

type duplicateScanStatus int

const (
	duplicateScanPending duplicateScanStatus = iota
	duplicateScanOK
	duplicateScanFailed
)

type duplicateScanState struct {
	status    duplicateScanStatus
	seen      map[int32]uint32
	remaining int
}

type pendingValidation struct {
	weight int64
}

type DuplicateScanJob struct {
	PocHeight      int64
	Participant    string
	ModelID        string
	ParticipantURL string
	RootHash       []byte
	Count          uint32
}

type duplicateScanChunk struct {
	fetcher proofFetcher
	job     DuplicateScanJob
	key     pocValidationKey
	indices []uint32
}

type duplicateNonceScanner struct {
	coordinator *PoCValidationCoordinator
	mu          sync.Mutex
	queue       []duplicateScanChunk
	inFlightURL map[string]bool
	nextURL     map[string]time.Time
	now         func() time.Time
}

type PoCValidationCoordinator struct {
	recorder     cosmosclient.CosmosMessageClient
	phaseTracker *chainphase.ChainPhaseTracker
	scanner      *duplicateNonceScanner

	mu        sync.Mutex
	scans     map[pocValidationKey]*duplicateScanState
	pending   map[pocValidationKey]pendingValidation
	submitted map[pocValidationKey]struct{}
}

func NewPoCValidationCoordinator(recorder cosmosclient.CosmosMessageClient, phaseTracker *chainphase.ChainPhaseTracker) *PoCValidationCoordinator {
	c := &PoCValidationCoordinator{
		recorder:     recorder,
		phaseTracker: phaseTracker,
		scans:        make(map[pocValidationKey]*duplicateScanState),
		pending:      make(map[pocValidationKey]pendingValidation),
		submitted:    make(map[pocValidationKey]struct{}),
	}
	c.scanner = newDuplicateNonceScanner(c)
	return c
}

func newDuplicateNonceScanner(coordinator *PoCValidationCoordinator) *duplicateNonceScanner {
	s := &duplicateNonceScanner{
		coordinator: coordinator,
		inFlightURL: make(map[string]bool),
		nextURL:     make(map[string]time.Time),
		now:         time.Now,
	}
	for i := 0; i < duplicateScanWorkers; i++ {
		go s.worker()
	}
	return s
}

func (c *PoCValidationCoordinator) StartDuplicateScan(fetcher proofFetcher, job DuplicateScanJob) {
	if c == nil || fetcher == nil || job.Count == 0 {
		return
	}

	indices, err := sampleDuplicateScanIndices(job.Count)
	if err != nil {
		logging.Warn("DuplicateNonceScanner: failed to sample random indices", types.PoC,
			"participant", job.Participant,
			"modelId", job.ModelID,
			"pocHeight", job.PocHeight,
			"count", job.Count,
			"error", err)
		return
	}
	if len(indices) == 0 {
		return
	}

	key := pocValidationKey{pocHeight: job.PocHeight, participant: job.Participant, modelID: job.ModelID}
	chunks := make([]duplicateScanChunk, 0, (len(indices)+duplicateScanChunkSize-1)/duplicateScanChunkSize)
	for start := 0; start < len(indices); start += duplicateScanChunkSize {
		end := start + duplicateScanChunkSize
		if end > len(indices) {
			end = len(indices)
		}
		chunkIndices := make([]uint32, end-start)
		copy(chunkIndices, indices[start:end])
		chunks = append(chunks, duplicateScanChunk{
			fetcher: fetcher,
			job:     job,
			key:     key,
			indices: chunkIndices,
		})
	}

	c.mu.Lock()
	c.scans[key] = &duplicateScanState{
		status:    duplicateScanPending,
		seen:      make(map[int32]uint32, len(indices)),
		remaining: len(chunks),
	}
	c.mu.Unlock()

	c.scanner.enqueue(chunks)
	logging.Info("DuplicateNonceScanner: queued scan", types.PoC,
		"participant", job.Participant,
		"modelId", job.ModelID,
		"pocHeight", job.PocHeight,
		"count", job.Count,
		"sampled", len(indices),
		"chunks", len(chunks))
}

func (c *PoCValidationCoordinator) HandleValidationResult(pocHeight int64, participant, modelID string, weight int64) error {
	if c == nil {
		return nil
	}
	key := pocValidationKey{pocHeight: pocHeight, participant: participant, modelID: modelID}
	if weight <= 0 {
		return c.submitAndMark(key, -1)
	}

	c.mu.Lock()
	if _, ok := c.submitted[key]; ok {
		c.mu.Unlock()
		return nil
	}
	if scan := c.scans[key]; scan != nil && scan.status == duplicateScanFailed {
		c.mu.Unlock()
		return c.submitAndMark(key, -1)
	}
	c.pending[key] = pendingValidation{weight: weight}
	c.mu.Unlock()

	c.ReleaseDue()
	return nil
}

func (c *PoCValidationCoordinator) ReleaseDue() int {
	if c == nil || !c.decisionReached() {
		return 0
	}

	c.mu.Lock()
	actions := make(map[pocValidationKey]int64)
	for key, pending := range c.pending {
		if _, ok := c.submitted[key]; ok {
			delete(c.pending, key)
			continue
		}
		if !c.isCurrentStageKey(key) {
			continue
		}
		weight := int64(-1)
		if scan := c.scans[key]; scan != nil && scan.status == duplicateScanOK {
			weight = pending.weight
		}
		actions[key] = weight
	}
	c.mu.Unlock()

	released := 0
	for key, weight := range actions {
		if err := c.submitAndMark(key, weight); err != nil {
			logging.Warn("PoCValidationCoordinator: failed to release validation", types.PoC,
				"pocHeight", key.pocHeight,
				"participant", key.participant,
				"modelId", key.modelID,
				"weight", weight,
				"error", err)
			continue
		}
		released++
	}
	return released
}

func (c *PoCValidationCoordinator) recordScanArtifacts(key pocValidationKey, verified []VerifiedArtifact) {
	var submitInvalid bool

	c.mu.Lock()
	scan := c.scans[key]
	if scan == nil || scan.status != duplicateScanPending {
		c.mu.Unlock()
		return
	}

	for _, artifact := range verified {
		if firstLeaf, exists := scan.seen[artifact.Nonce]; exists && firstLeaf != artifact.LeafIndex {
			scan.status = duplicateScanFailed
			submitInvalid = true
			break
		}
		scan.seen[artifact.Nonce] = artifact.LeafIndex
	}
	if scan.status == duplicateScanPending {
		scan.remaining--
		if scan.remaining <= 0 {
			scan.status = duplicateScanOK
		}
	}
	c.mu.Unlock()

	if submitInvalid {
		logging.Warn("DuplicateNonceScanner: duplicate nonce found", types.PoC,
			"pocHeight", key.pocHeight,
			"participant", key.participant,
			"modelId", key.modelID)
		if err := c.submitAndMark(key, -1); err != nil {
			logging.Warn("DuplicateNonceScanner: failed to submit invalid vote", types.PoC,
				"pocHeight", key.pocHeight,
				"participant", key.participant,
				"modelId", key.modelID,
				"error", err)
		}
		return
	}

	c.ReleaseDue()
}

func (c *PoCValidationCoordinator) submitAndMark(key pocValidationKey, weight int64) error {
	c.mu.Lock()
	if _, ok := c.submitted[key]; ok {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	msg := &types.MsgSubmitPocValidationsV2{
		PocStageStartBlockHeight: key.pocHeight,
		Validations: []*types.PoCValidationEntryV2{
			{
				ParticipantAddress: key.participant,
				ModelId:            key.modelID,
				ValidatedWeight:    weight,
			},
		},
	}
	if c.recorder != nil {
		if err := c.recorder.SubmitPocValidationsV2(msg); err != nil {
			return err
		}
	}

	c.mu.Lock()
	c.submitted[key] = struct{}{}
	delete(c.pending, key)
	c.mu.Unlock()
	return nil
}

func (c *PoCValidationCoordinator) decisionReached() bool {
	if c.phaseTracker == nil {
		return true
	}
	state := c.phaseTracker.GetCurrentEpochState()
	return PoCValidationDecisionReached(state)
}

func (c *PoCValidationCoordinator) isCurrentStageKey(key pocValidationKey) bool {
	if c.phaseTracker == nil {
		return true
	}
	state := c.phaseTracker.GetCurrentEpochState()
	return GetCurrentPocStageHeight(state) == key.pocHeight
}

func (s *duplicateNonceScanner) enqueue(chunks []duplicateScanChunk) {
	if s == nil || len(chunks) == 0 {
		return
	}
	s.mu.Lock()
	s.queue = append(s.queue, chunks...)
	s.mu.Unlock()
}

func (s *duplicateNonceScanner) worker() {
	for {
		chunk, ok := s.nextChunk()
		if !ok {
			time.Sleep(duplicateScanPollInterval)
			continue
		}
		s.runChunk(chunk)
	}
}

func (s *duplicateNonceScanner) nextChunk() (duplicateScanChunk, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for i, chunk := range s.queue {
		if s.inFlightURL[chunk.job.ParticipantURL] {
			continue
		}
		if next := s.nextURL[chunk.job.ParticipantURL]; next.After(now) {
			continue
		}
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		s.inFlightURL[chunk.job.ParticipantURL] = true
		s.nextURL[chunk.job.ParticipantURL] = now.Add(duplicateScanURLCooldown)
		return chunk, true
	}
	return duplicateScanChunk{}, false
}

func (s *duplicateNonceScanner) runChunk(chunk duplicateScanChunk) {
	defer func() {
		s.mu.Lock()
		delete(s.inFlightURL, chunk.job.ParticipantURL)
		s.mu.Unlock()
	}()

	verified, err := chunk.fetcher.FetchAndVerifyProofs(context.Background(), chunk.job.ParticipantURL, ProofRequest{
		PocStageStartBlockHeight: chunk.job.PocHeight,
		ModelId:                  chunk.job.ModelID,
		RootHash:                 chunk.job.RootHash,
		Count:                    chunk.job.Count,
		LeafIndices:              chunk.indices,
		ParticipantAddress:       chunk.job.Participant,
	})
	if err != nil {
		logging.Warn("DuplicateNonceScanner: proof chunk failed", types.PoC,
			"pocHeight", chunk.job.PocHeight,
			"participant", chunk.job.Participant,
			"modelId", chunk.job.ModelID,
			"error", err)
		s.mu.Lock()
		s.queue = append(s.queue, chunk)
		s.mu.Unlock()
		return
	}

	s.coordinator.recordScanArtifacts(chunk.key, verified)
}

// sampleDuplicateScanIndices uses private local randomness so participants cannot
// precompute which committed leaves this validator will probe from public chain data.
func sampleDuplicateScanIndices(count uint32) ([]uint32, error) {
	if count == 0 {
		return nil, nil
	}
	sampleSize := int(count)
	if sampleSize > duplicateScanSampleSize {
		sampleSize = duplicateScanSampleSize
	}

	swaps := make(map[uint32]uint32, sampleSize*2)
	get := func(i uint32) uint32 {
		if v, ok := swaps[i]; ok {
			return v
		}
		return i
	}

	result := make([]uint32, sampleSize)
	n := int(count)
	for i := 0; i < sampleSize; i++ {
		offset, err := crand.Int(crand.Reader, big.NewInt(int64(n-i)))
		if err != nil {
			return nil, err
		}
		j := i + int(offset.Int64())
		ii := uint32(i)
		jj := uint32(j)

		vi := get(ii)
		vj := get(jj)
		swaps[ii] = vj
		swaps[jj] = vi
		result[i] = vj
	}
	return result, nil
}
