package poc

import (
	"context"
	crand "crypto/rand"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	duplicateScanSampleSize   = 5_000
	duplicateScanChunkSize    = 500
	duplicateScanURLCooldown  = 5 * time.Second
	duplicateScanWorkers      = 20
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
	duplicateScanAbstained
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
	if scan := c.scans[key]; scan != nil && scan.status == duplicateScanAbstained {
		c.mu.Unlock()
		return nil
	}
	c.pending[key] = pendingValidation{weight: weight}
	c.mu.Unlock()

	c.ReleaseDue()
	return nil
}

func (c *PoCValidationCoordinator) ReleaseDue() int {
	if c == nil {
		return 0
	}
	c.pruneStaleStages()
	if !c.decisionReached() {
		return 0
	}

	c.mu.Lock()
	actions := make(map[pocValidationKey]int64)
	abstained := make([]pocValidationKey, 0)
	for key, pending := range c.pending {
		if _, ok := c.submitted[key]; ok {
			delete(c.pending, key)
			continue
		}
		if !c.isCurrentStageKey(key) {
			continue
		}
		scan := c.scans[key]
		switch {
		case scan != nil && scan.status == duplicateScanOK:
			actions[key] = pending.weight
		case scan != nil && scan.status == duplicateScanFailed:
			actions[key] = -1
		default:
			if scan != nil {
				scan.status = duplicateScanAbstained
			} else {
				c.scans[key] = &duplicateScanState{status: duplicateScanAbstained}
			}
			delete(c.pending, key)
			abstained = append(abstained, key)
		}
	}
	c.mu.Unlock()

	if len(abstained) > 0 && c.scanner != nil {
		c.scanner.pruneQueue()
	}
	for _, key := range abstained {
		logging.Warn("PoCValidationCoordinator: abstaining from validation; duplicate scan incomplete", types.PoC,
			"pocHeight", key.pocHeight,
			"participant", key.participant,
			"modelId", key.modelID)
	}

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

	// The vote is final; any still-queued scan chunks for this key are useless.
	if c.scanner != nil {
		c.scanner.pruneQueue()
	}
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

// pruneStaleStages drops coordinator state and queued scan chunks belonging to
// stages other than the current one, so memory does not grow across epochs.
func (c *PoCValidationCoordinator) pruneStaleStages() {
	if c.phaseTracker == nil {
		return
	}
	currentHeight := GetCurrentPocStageHeight(c.phaseTracker.GetCurrentEpochState())
	if currentHeight == 0 {
		return
	}

	c.mu.Lock()
	for key := range c.scans {
		if key.pocHeight != currentHeight {
			delete(c.scans, key)
		}
	}
	for key := range c.pending {
		if key.pocHeight != currentHeight {
			delete(c.pending, key)
		}
	}
	for key := range c.submitted {
		if key.pocHeight != currentHeight {
			delete(c.submitted, key)
		}
	}
	c.mu.Unlock()

	if c.scanner != nil {
		c.scanner.pruneQueue()
	}
}

// markScanFailed records a permanent scan failure so a later positive ML
// validation result is converted to an invalid vote by the release logic.
func (c *PoCValidationCoordinator) markScanFailed(key pocValidationKey, reason string) {
	c.mu.Lock()
	scan := c.scans[key]
	if scan != nil && scan.status == duplicateScanPending {
		scan.status = duplicateScanFailed
	}
	c.mu.Unlock()

	logging.Warn("DuplicateNonceScanner: scan failed permanently", types.PoC,
		"pocHeight", key.pocHeight,
		"participant", key.participant,
		"modelId", key.modelID,
		"reason", reason)
	if c.scanner != nil {
		c.scanner.pruneQueue()
	}
}

// isScanPending reports whether more scan chunks are still useful for this
// key: the scan must exist, be undecided, and no vote must have been submitted.
func (c *PoCValidationCoordinator) isScanPending(key pocValidationKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, submitted := c.submitted[key]; submitted {
		return false
	}
	scan := c.scans[key]
	return scan != nil && scan.status == duplicateScanPending
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
	for i := 0; i < len(s.queue); {
		chunk := s.queue[i]
		// Drop chunks whose scan is already decided or was pruned with its stage.
		if s.coordinator != nil && !s.coordinator.isScanPending(chunk.key) {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			continue
		}
		if s.inFlightURL[chunk.job.ParticipantURL] {
			i++
			continue
		}
		if next := s.nextURL[chunk.job.ParticipantURL]; next.After(now) {
			i++
			continue
		}
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		s.inFlightURL[chunk.job.ParticipantURL] = true
		return chunk, true
	}
	return duplicateScanChunk{}, false
}

// pruneQueue removes chunks for scans that are already decided or belong to
// pruned stages, so they neither waste requests nor accumulate over time.
func (s *duplicateNonceScanner) pruneQueue() {
	if s == nil || s.coordinator == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dst := s.queue[:0]
	for _, chunk := range s.queue {
		if s.coordinator.isScanPending(chunk.key) {
			dst = append(dst, chunk)
		}
	}
	s.queue = dst
}

func (s *duplicateNonceScanner) runChunk(chunk duplicateScanChunk) {
	defer func() {
		s.mu.Lock()
		delete(s.inFlightURL, chunk.job.ParticipantURL)
		s.nextURL[chunk.job.ParticipantURL] = s.now().Add(duplicateScanURLCooldown)
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
		if !isRetryableDuplicateScanError(err) {
			s.coordinator.markScanFailed(chunk.key, err.Error())
			return
		}
		// Requeue at the back only while the scan is still undecided; the URL
		// cooldown set when requests finish keeps retries at most once per 5s per URL.
		if s.coordinator == nil || s.coordinator.isScanPending(chunk.key) {
			s.requeueParticipantAtBack(chunk)
		}
		return
	}

	s.coordinator.recordScanArtifacts(chunk.key, verified)
}

func (s *duplicateNonceScanner) requeueParticipantAtBack(failed duplicateScanChunk) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	retained := s.queue[:0]
	moved := make([]duplicateScanChunk, 0)
	for _, chunk := range s.queue {
		if s.coordinator != nil && !s.coordinator.isScanPending(chunk.key) {
			continue
		}
		if sameScanOrURL(chunk, failed) {
			moved = append(moved, chunk)
			continue
		}
		retained = append(retained, chunk)
	}
	s.queue = retained
	s.queue = append(s.queue, moved...)
	if s.coordinator == nil || s.coordinator.isScanPending(failed.key) {
		s.queue = append(s.queue, failed)
	}
}

func sameScanOrURL(a, b duplicateScanChunk) bool {
	return a.key == b.key || a.job.ParticipantURL == b.job.ParticipantURL
}

// isRetryableDuplicateScanError classifies proof fetch failures. Typed proof
// errors and non-429 4xx responses are permanent participant faults; transport
// failures, rate limits, server errors, and unknown errors are retried so a
// transient hiccup never marks a scan failed. Retries are bounded by the
// scan-pending check and stale-stage pruning.
func isRetryableDuplicateScanError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrProofVerificationFailed) ||
		errors.Is(err, ErrIncompleteCoverage) ||
		errors.Is(err, ErrInvalidVectorData) {
		return false
	}
	if status := proofRequestStatus(err); status != 0 {
		return status == 429 || status >= 500
	}
	return true
}

// proofRequestStatus extracts the HTTP status from ProofClient's
// "proof request failed with status N" error message, or 0 if absent.
func proofRequestStatus(err error) int {
	const marker = "proof request failed with status "
	message := strings.ToLower(err.Error())
	start := strings.Index(message, marker)
	if start < 0 {
		return 0
	}
	rest := message[start+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	status, _ := strconv.Atoi(rest[:end])
	return status
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
