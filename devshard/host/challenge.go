package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"common/completionapi"

	"devshard"
	"devshard/logging"
	"devshard/observability"
	"devshard/storage"
	"devshard/types"
)

// Reject causes for VerifyChallenge. A reject carries no vote.
const (
	ChallengeRejectNotLive     = "not_live"
	ChallengeRejectNotFinished = "not_finished"
	ChallengeRejectChallenged  = "already_usage_challenged"
	ChallengeRejectExecutor    = "executor"
	ChallengeRejectUnsupported = "unsupported"
	ChallengeRejectUnchecked   = "unchecked"
	ChallengeRejectLate        = "late"
)

// challengeFirstAskLimit bounds how long after the Finish a gateway may first
// challenge it and still get the fetch window.
const challengeFirstAskLimit = 5 * time.Minute

var (
	// challengeFetchRetryInterval is the pause between fetch attempts inside
	// the window. Overridable in tests.
	challengeFetchRetryInterval = 10 * time.Second
	// challengeAnswerWait is how long one VerifyChallenge waits for the fetch
	// before it answers pending. It stays well under the peer client's verify
	// timeout. Overridable in tests.
	challengeAnswerWait = 20 * time.Second
	// challengeFetchGrace is how long a fetch attempt started inside the
	// window may run past the window's end before it is cut. One attempt
	// retries inside the peer client and waits out an executor's Retry-After,
	// up to a few minutes; the gateway stops asking for the vote two minutes
	// after the window, so a vote settled later than that is lost. Overridable
	// in tests.
	challengeFetchGrace = time.Minute
)

// ChallengeAnswer is a host's reply to VerifyChallenge: pending while its
// fetch is open, then the signed vote it keeps for the inference, or a reject
// cause and no vote.
type ChallengeAnswer struct {
	Pending     bool
	Fault       types.ChallengeFault
	Check       types.ChallengeCheck
	Deviation   int64
	Signature   []byte
	VoterSlot   uint32
	RejectCause string
}

// keptChallengeVote is what a host stores per challenged inference. Deadline
// is the end of the fetch window, fixed when the entry is first stored.
type keptChallengeVote struct {
	Deadline  int64                `json:"deadline"`
	Fault     types.ChallengeFault `json:"fault,omitempty"`
	Check     types.ChallengeCheck `json:"check,omitempty"`
	Deviation int64                `json:"deviation,omitempty"`
	Signature []byte               `json:"signature,omitempty"`
	VoterSlot uint32               `json:"voter_slot,omitempty"`
}

func (k keptChallengeVote) answer() ChallengeAnswer {
	return ChallengeAnswer{
		Fault:     k.Fault,
		Check:     k.Check,
		Deviation: k.Deviation,
		Signature: append([]byte(nil), k.Signature...),
		VoterSlot: k.VoterSlot,
	}
}

type challengeJob struct {
	done   chan struct{}
	answer ChallengeAnswer
	err    error
}

// challengeVoter runs at most one fetch per inference and remembers nothing
// itself: the kept votes live in the store, so they survive a restart.
type challengeVoter struct {
	mu        sync.Mutex
	store     storage.ChallengeVoteStore
	jobs      map[uint64]*challengeJob
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	workers   sync.WaitGroup
	fallbacks *storage.Memory
}

func (c *challengeVoter) close() {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.workers.Wait()
}

// challengeVoteStore is the store's challenge vote table, or a process-local map for
// a host without durable storage.
func (h *Host) challengeVoteStore() storage.ChallengeVoteStore {
	c := &h.challenges
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store != nil {
		return c.store
	}
	if store, ok := storage.AsChallengeVoteStore(h.store); ok {
		c.store = store
		return store
	}
	if c.fallbacks == nil {
		c.fallbacks = storage.NewMemory()
	}
	c.store = c.fallbacks
	return c.store
}

// challengeWindow is the fetch window for this session's seal grace.
func (h *Host) challengeWindow() time.Duration {
	return types.ChallengeWindow(h.sm.Config().InferenceSealGraceSeconds)
}

// challengeFirstAsk is how long after the Finish a first challenge request
// opens the fetch window: challengeFirstAskLimit, or half a shorter window.
func (h *Host) challengeFirstAsk() time.Duration {
	return min(challengeFirstAskLimit, h.challengeWindow()/2)
}

// challengeAnchor is when this host applied the inference's Finish, or now
// for a host that learns the Finish only from the challenge's own diffs.
func (h *Host) challengeAnchor(inferenceID uint64, now time.Time) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	if obs, ok := h.finishObs[inferenceID]; ok && obs.at.Before(now) {
		return obs.at
	}
	return now
}

// VerifyChallenge answers a gateway's challenge of a finished inference with
// this host's kept vote. The first request opens a fetch of the body from the
// executor, retried until a window anchored at the Finish ends; later
// requests and a request after a restart get the same vote, and pending while
// the fetch is open. The vote depends on the body alone, not on the reason
// the gateway gave, so the reason stays on the wire for the logs and is not
// read here. Nothing here calls a model.
func (h *Host) VerifyChallenge(ctx context.Context, inferenceID uint64) (ChallengeAnswer, error) {
	rec, ok := h.sm.GetInference(inferenceID)
	if !ok {
		return ChallengeAnswer{RejectCause: ChallengeRejectNotLive}, nil
	}
	if rec.UsageChallenged {
		return ChallengeAnswer{RejectCause: ChallengeRejectChallenged}, nil
	}
	if rec.Status != types.StatusFinished {
		return ChallengeAnswer{RejectCause: ChallengeRejectNotFinished}, nil
	}
	if h.slotToAddr[rec.ExecutorSlot] == h.slotToAddr[h.PrimarySlot()] {
		return ChallengeAnswer{RejectCause: ChallengeRejectExecutor}, nil
	}
	fetcher, ok := h.validator.(devshard.ChallengeFetcher)
	if !ok {
		return ChallengeAnswer{RejectCause: ChallengeRejectUnsupported}, nil
	}

	store := h.challengeVoteStore()
	now := time.Now()
	anchor := h.challengeAnchor(inferenceID, now)
	deadline := anchor.Add(h.challengeWindow())
	// A gateway is not trusted to challenge promptly. One that first asks
	// late, say while the executor restarts, gets no window: a fetch that
	// fails then is no vote, so the voters are never left seconds to fetch.
	if now.Sub(anchor) > h.challengeFirstAsk() {
		deadline = now
	}
	pending, err := json.Marshal(keptChallengeVote{Deadline: deadline.Unix()})
	if err != nil {
		return ChallengeAnswer{}, err
	}
	data, settled, err := store.LoadOrStoreChallengeVote(h.escrowID, inferenceID, pending)
	if err != nil {
		return ChallengeAnswer{}, fmt.Errorf("challenge vote store: %w", err)
	}
	var kept keptChallengeVote
	if err := json.Unmarshal(data, &kept); err != nil {
		return ChallengeAnswer{}, fmt.Errorf("decode kept challenge vote: %w", err)
	}
	if settled {
		return kept.answer(), nil
	}

	job, started, err := h.challengeJobFor(inferenceID, func(ctx context.Context) (ChallengeAnswer, error) {
		return h.runChallengeFetch(ctx, fetcher, store, inferenceID, rec, time.Unix(kept.Deadline, 0))
	})
	if err != nil {
		return ChallengeAnswer{}, err
	}
	if !started {
		// The fetch is already running: answer at once so the ask does not
		// hold a slot of the gateway's verifier queue for the wait.
		return ChallengeAnswer{Pending: true}, nil
	}
	wait := time.NewTimer(challengeAnswerWait)
	defer wait.Stop()
	select {
	case <-job.done:
		return job.answer, job.err
	case <-wait.C:
		return ChallengeAnswer{Pending: true}, nil
	case <-ctx.Done():
		return ChallengeAnswer{Pending: true}, nil
	}
}

// challengeJobFor returns the running fetch for inferenceID, or starts one and
// reports so. A finished job is forgotten, so a request after an unchecked
// answer runs the fetch again with the stored deadline.
func (h *Host) challengeJobFor(inferenceID uint64, run func(context.Context) (ChallengeAnswer, error)) (*challengeJob, bool, error) {
	c := &h.challenges
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false, fmt.Errorf("host closed")
	}
	if job, ok := c.jobs[inferenceID]; ok {
		return job, false, nil
	}
	if c.ctx == nil {
		c.ctx, c.cancel = context.WithCancel(context.Background())
	}
	if c.jobs == nil {
		c.jobs = make(map[uint64]*challengeJob)
	}
	job := &challengeJob{done: make(chan struct{})}
	c.jobs[inferenceID] = job
	ctx := c.ctx
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		job.answer, job.err = run(ctx)
		c.mu.Lock()
		delete(c.jobs, inferenceID)
		c.mu.Unlock()
		close(job.done)
	}()
	return job, true, nil
}

// runChallengeFetch fetches until a verdict or the deadline. A failure that
// may clear is retried inside the window; one still failing at the deadline
// is the executor's fault only if it was the executor's side and this host
// had tried before the window ended. An attempt still running
// challengeFetchGrace past the deadline is cut and counts as the executor
// not answering inside the window. A fetch this host cannot run, or one it
// first tried after the window, gives no vote.
func (h *Host) runChallengeFetch(
	ctx context.Context,
	fetcher devshard.ChallengeFetcher,
	store storage.ChallengeVoteStore,
	inferenceID uint64,
	rec types.InferenceRecord,
	deadline time.Time,
) (ChallengeAnswer, error) {
	req := devshard.ValidateRequest{
		InferenceID:     inferenceID,
		Model:           rec.Model,
		PromptHash:      rec.PromptHash,
		ResponseHash:    rec.ResponseHash,
		ServedHash:      rec.ServedHash,
		InputTokens:     rec.InputTokens,
		OutputTokens:    rec.OutputTokens,
		EscrowID:        h.escrowID,
		ExecutorAddress: h.slotToAddr[rec.ExecutorSlot],
		EpochID:         h.epochID,
	}
	triedInWindow := time.Now().Before(deadline)
	fetchCtx, cutFetch := context.WithDeadline(ctx, deadline.Add(challengeFetchGrace))
	defer cutFetch()
	attempts := 0
	for {
		attempts++
		fetch := fetcher.FetchChallengePayload(fetchCtx, req)
		if ctx.Err() != nil {
			return ChallengeAnswer{Pending: true}, nil
		}
		if fetchCtx.Err() != nil {
			fetch = devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchRetry, ExecutorSide: true, Cause: "executor did not answer the fetch inside the window"}
		}
		if verdict, ok := challengeVerdict(fetch, rec); ok {
			return h.settleChallengeVote(store, inferenceID, rec.ResponseHash, verdict, attempts, fetch.Cause)
		}
		if fetch.Outcome == devshard.ChallengeFetchLocal {
			return h.uncheckedChallenge(inferenceID, attempts, fetch.Cause), nil
		}
		now := time.Now()
		if !now.Before(deadline) {
			if fetch.ExecutorSide && triedInWindow {
				verdict := keptChallengeVote{Fault: types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE}
				return h.settleChallengeVote(store, inferenceID, rec.ResponseHash, verdict, attempts, fetch.Cause)
			}
			if !triedInWindow {
				return h.noChallengeVote(inferenceID, ChallengeRejectLate, attempts, fetch.Cause), nil
			}
			return h.uncheckedChallenge(inferenceID, attempts, fetch.Cause), nil
		}
		pause := min(challengeFetchRetryInterval, deadline.Sub(now))
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ChallengeAnswer{Pending: true}, nil
		case <-timer.C:
		}
	}
}

// challengeVerdict maps a fetch that settles the vote. ok is false for a
// failure that may still clear. The vote depends on the body alone, not on
// the reason the gateway gave: one kept vote per inference must hold for
// whichever challenge arrives first.
func challengeVerdict(fetch devshard.ChallengeFetch, rec types.InferenceRecord) (keptChallengeVote, bool) {
	switch fetch.Outcome {
	case devshard.ChallengeFetchFetched:
		// Content is the generated positions the stored body keeps, one per
		// token, not the decoded text: an honest answer can decode to
		// nothing, but the executor's devshardd forces logprobs on every
		// request it runs, so it never has zero positions. The exception is the reasoning model
		// whose tokens vLLM strips with the reasoning block: with completion
		// tokens claimed, no positions is its documented outcome, which the
		// gateway does not quarantine and replay passes as both empty.
		if generatedPositions(fetch.Response) == 0 && !(types.ModelBurnsEmpty(rec.Model) && rec.OutputTokens > 0) {
			return keptChallengeVote{
				Fault:     types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM,
				Check:     types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS,
				Deviation: int64(rec.OutputTokens),
			}, true
		}
		return keptChallengeVote{Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE}, true
	case devshard.ChallengeFetchMismatch:
		return keptChallengeVote{
			Fault: types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH,
			Check: types.ChallengeCheck_CHALLENGE_CHECK_HASH,
		}, true
	case devshard.ChallengeFetchUnavailable:
		return keptChallengeVote{Fault: types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE}, true
	case devshard.ChallengeFetchPruned:
		return keptChallengeVote{Fault: types.ChallengeFault_CHALLENGE_FAULT_NONE}, true
	default:
		return keptChallengeVote{}, false
	}
}

// generatedPositions counts the logprobs.content entries of a stored body.
func generatedPositions(response []byte) int {
	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(response)
	if err != nil || resp == nil {
		return 0
	}
	return len(resp.ExtractLogits())
}

func (h *Host) settleChallengeVote(store storage.ChallengeVoteStore, inferenceID uint64, responseHash []byte, verdict keptChallengeVote, attempts int, cause string) (ChallengeAnswer, error) {
	content := &types.ChallengeVoteContent{
		EscrowId:     h.escrowID,
		InferenceId:  inferenceID,
		Fault:        verdict.Fault,
		Check:        verdict.Check,
		Deviation:    verdict.Deviation,
		ResponseHash: responseHash,
	}
	preimage, err := types.CanonicalSignedBytes(content)
	if err != nil {
		return ChallengeAnswer{}, fmt.Errorf("marshal challenge vote: %w", err)
	}
	sig, err := h.signer.Sign(preimage)
	if err != nil {
		return ChallengeAnswer{}, fmt.Errorf("sign challenge vote: %w", err)
	}
	verdict.Signature = sig
	verdict.VoterSlot = h.PrimarySlot()
	data, err := json.Marshal(verdict)
	if err != nil {
		return ChallengeAnswer{}, err
	}
	stored, err := store.SettleChallengeVote(h.escrowID, inferenceID, data)
	if err != nil {
		return ChallengeAnswer{}, fmt.Errorf("challenge vote store: %w", err)
	}
	var kept keptChallengeVote
	if err := json.Unmarshal(stored, &kept); err != nil {
		return ChallengeAnswer{}, fmt.Errorf("decode kept challenge vote: %w", err)
	}
	if bytes.Equal(stored, data) {
		// An HA replica that lost the first settle keeps the winner's vote
		// and does not count it again.
		observability.IncChallengeVote(types.ChallengeFaultLabel(kept.Fault))
	}
	logging.Info("challenge vote kept", "subsystem", "host",
		"escrow_id", h.escrowID,
		"inference_id", inferenceID,
		"fault", kept.Fault.String(),
		"check", kept.Check.String(),
		"attempts", attempts,
		"cause", cause,
	)
	return kept.answer(), nil
}

func (h *Host) uncheckedChallenge(inferenceID uint64, attempts int, cause string) ChallengeAnswer {
	return h.noChallengeVote(inferenceID, ChallengeRejectUnchecked, attempts, cause)
}

func (h *Host) noChallengeVote(inferenceID uint64, reject string, attempts int, cause string) ChallengeAnswer {
	observability.IncChallengeVote(reject)
	logging.Warn("challenge left unchecked", "subsystem", "host",
		"escrow_id", h.escrowID,
		"inference_id", inferenceID,
		"reject", reject,
		"attempts", attempts,
		"cause", cause,
	)
	return ChallengeAnswer{RejectCause: reject}
}
