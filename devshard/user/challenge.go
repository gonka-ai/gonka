package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"devshard/logging"
	"devshard/transport"
	"devshard/types"
)

// Gateway side of the usage challenge (gonka-ai/gonka#2000). A stream that
// does not bind to the applied Finish, or a receipt-backed stream that came
// back empty, makes the gateway ask every other host of the escrow for its
// verdict and put the signed votes into the next diff as MsgChallenge.

var (
	// ChallengeReaskInterval is the pause before asking a host again after an
	// error: a 5xx, a host not attached yet. A 429 or 503 waits the pending
	// interval, or the host's Retry-After when that is longer.
	ChallengeReaskInterval = 5 * time.Second
	// ChallengePendingReaskInterval is the pause after a host answered
	// pending: its fetch is running and takes minutes, and every ask holds a
	// slot of the verifier queue the timeout and error-miss votes share.
	ChallengePendingReaskInterval = 30 * time.Second
	// ChallengeVoteSlack is added to the session's fetch window when the
	// gateway decides how long to keep asking. A host's window ends a window
	// after its own Finish apply, which is before the gateway's challenge
	// starts, and its last fetch attempt may run a minute past that end
	// before the host cuts it and settles the vote.
	ChallengeVoteSlack = 2 * time.Minute
	// ChallengeOpenStreamGrace is how long an attempt's stream may stay open
	// after its Finish applied before the gateway challenges it without
	// waiting for the stream to end. The executor signs the Finish over the
	// whole body, so an honest stream ends seconds after it, not minutes;
	// one that drags on past the hosts' first-ask limit would make the
	// gateway's challenge late by the executor's choice. Only the time the
	// gateway waited on the executor counts: the relay to the gateway's own
	// client has no buffer, so a client that reads slowly holds the stream
	// open too, and that wait is not the executor's.
	ChallengeOpenStreamGrace = time.Minute
)

// ChallengeHandler starts a challenge the session decided on. It is called
// with the session lock held, so it must hand the work to a goroutine. The
// gateway runs RunChallenge under its settle barrier.
type ChallengeHandler func(nonce uint64, reason types.ChallengeReason)

// ChallengeResult is what one challenge produced.
type ChallengeResult struct {
	Nonce  uint64
	Reason types.ChallengeReason
	// ExecutorIdx is the executor's host index, or -1 when the record is
	// unknown.
	ExecutorIdx int
	// Votes holds the verified signed votes sent in MsgChallenge.
	Votes []*types.ChallengeVote
	// Faults is the slot weight of votes that found a fault.
	Faults uint32
	// Unanswered counts hosts that gave no verdict: errors, still pending at
	// the deadline, a reject cause, or not awaited once the outcome was
	// settled. UnansweredWeight is their slot weight.
	Unanswered       int
	UnansweredWeight uint32
	// Threshold is the session's vote threshold: a fault weight above it
	// invalidates the record.
	Threshold   uint32
	Invalidated bool
	// UnsupportedProtocol is set when the session runs a protocol before
	// MsgChallenge, as an escrow of the previous release does: its hosts do
	// not know the tx, so nothing is asked.
	UnsupportedProtocol bool
	// NonceCap is set when no diff could carry the MsgChallenge: the session
	// is at the active nonce cap or in a finalize round. Nothing is asked.
	NonceCap bool
	// ReplayFirst is set when a replay vote reached the record before the
	// challenge tx did: the replay flow owns the record now.
	ReplayFirst bool
}

type challengeMark struct {
	reason   types.ChallengeReason
	markedAt time.Time
}

// SetChallengeHandler installs the callback that runs challenges.
func (s *Session) SetChallengeHandler(handler ChallengeHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challengeHandler = handler
}

// MarkChallengeStream records that the stream of inference nonce did not
// bind: it ended before [DONE] and devshard_meta (STREAM_UNBOUND), or it was a
// receipt-backed success with no content (EMPTY_STREAM). The challenge starts
// when the Finish applies, or now when the applied Finish is already known.
// Callers leave out probes, phase-change aborts and streams the gateway cut
// itself before any receipt.
func (s *Session) MarkChallengeStream(nonce uint64, reason types.ChallengeReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streamsOpen, nonce)
	if _, applied := s.challengeFinishes[nonce]; applied {
		// The stream ended without binding, so no stream is left to judge
		// the applied Finish against.
		delete(s.appliedFinishHashes, nonce)
		s.startChallengeLocked(nonce, reason)
		return
	}
	if _, started := s.challengeStarted[nonce]; started {
		return
	}
	if prev, ok := s.challengeMarks[nonce]; ok && prev.reason == types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM {
		return
	}
	if s.challengeMarks == nil {
		s.challengeMarks = make(map[uint64]challengeMark)
	}
	now := s.nowLocked()
	s.challengeMarks[nonce] = challengeMark{reason: reason, markedAt: now}
	s.forgetExpiredServedBindingsLocked(now)
}

// openStream is an attempt stream the gateway is reading. clientWait reports
// how long the gateway has stood in writes to its own client on this stream,
// time the executor does not drive; nil when the caller cannot tell.
type openStream struct {
	clientWait func() time.Duration
}

func (o openStream) clientWaited() time.Duration {
	if o.clientWait == nil {
		return 0
	}
	return o.clientWait()
}

// StreamOpened records that the gateway is reading the attempt stream of
// inference nonce. It is paired with MarkChallengeStream, BindReceivedStream
// or StreamEnded when the stream ends. clientWait is read when the stream
// outlives its Finish; see openStream.
func (s *Session) StreamOpened(nonce uint64, clientWait func() time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streamsOpen == nil {
		s.streamsOpen = make(map[uint64]openStream)
	}
	s.streamsOpen[nonce] = openStream{clientWait: clientWait}
}

// StreamEnded records that the stream of inference nonce ended with nothing
// to challenge: a probe, a phase-change abort, or a cut the gateway made
// before any receipt.
func (s *Session) StreamEnded(nonce uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streamsOpen, nonce)
	if s.streamsEnded == nil {
		s.streamsEnded = make(map[uint64]time.Time)
	}
	s.streamsEnded[nonce] = s.nowLocked()
}

// challengeFinishAppliedLocked joins an applied Finish with a waiting mark.
// It reports whether the inference is challenged, now or before. A Finish
// whose stream is still open arms a challenge for when the stream outlives
// ChallengeOpenStreamGrace.
func (s *Session) challengeFinishAppliedLocked(nonce uint64) bool {
	if mark, ok := s.challengeMarks[nonce]; ok {
		s.startChallengeLocked(nonce, mark.reason)
		return true
	}
	if _, started := s.challengeStarted[nonce]; started {
		return true
	}
	if s.challengeFinishes == nil {
		s.challengeFinishes = make(map[uint64]time.Time)
	}
	s.challengeFinishes[nonce] = s.nowLocked()
	if stream, open := s.streamsOpen[nonce]; open {
		s.armOpenStreamChallenge(nonce, time.Now(), stream.clientWaited(), ChallengeOpenStreamGrace)
	}
	return false
}

// armOpenStreamChallenge checks the stream of nonce again after wait. since
// is when its Finish applied and clientWaitAtFinish the client wait the
// stream had accrued by then.
func (s *Session) armOpenStreamChallenge(nonce uint64, since time.Time, clientWaitAtFinish, wait time.Duration) {
	time.AfterFunc(wait, func() { s.challengeOpenStream(nonce, since, clientWaitAtFinish) })
}

// challengeOpenStream fires once the grace since a Finish applied with its
// stream open has run out. A stream still open then is challenged as not
// bound; one that ended since was handled on its own way out. The time the
// gateway stood in writes to its own client since the Finish is not the
// executor's: it is taken out of the grace, and the stream is checked again
// when the rest has passed.
func (s *Session) challengeOpenStream(nonce uint64, since time.Time, clientWaitAtFinish time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stream, open := s.streamsOpen[nonce]
	if !open {
		return
	}
	if _, applied := s.challengeFinishes[nonce]; !applied {
		return
	}
	clientWait := stream.clientWaited() - clientWaitAtFinish
	if onExecutor := time.Since(since) - clientWait; onExecutor < ChallengeOpenStreamGrace {
		s.armOpenStreamChallenge(nonce, since, clientWaitAtFinish, ChallengeOpenStreamGrace-onExecutor)
		return
	}
	logging.Info("challenge_stream_open_after_finish", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
		"grace_ms", ChallengeOpenStreamGrace.Milliseconds(), "client_wait_ms", clientWait.Milliseconds())
	s.startChallengeLocked(nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
}

// startChallengeLocked hands the challenge to the handler once. The applied
// Finish hashes stay: a stream challenged for outliving the grace still ends,
// and its hashes are then judged against the Finish as any other stream's.
func (s *Session) startChallengeLocked(nonce uint64, reason types.ChallengeReason) {
	delete(s.challengeMarks, nonce)
	delete(s.challengeFinishes, nonce)
	if _, started := s.challengeStarted[nonce]; started {
		return
	}
	if s.challengeStarted == nil {
		s.challengeStarted = make(map[uint64]time.Time)
	}
	s.challengeStarted[nonce] = s.nowLocked()
	logging.Info("challenge_triggered", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
		"reason", types.ChallengeReasonLabel(reason))
	if s.challengeHandler != nil {
		s.challengeHandler(nonce, reason)
	}
}

func (s *Session) forgetExpiredChallengesLocked(now time.Time, retention time.Duration) {
	for nonce, mark := range s.challengeMarks {
		if now.Sub(mark.markedAt) > retention && !s.finishCanStillApplyLocked(nonce) {
			delete(s.challengeMarks, nonce)
		}
	}
	for nonce, appliedAt := range s.challengeFinishes {
		if now.Sub(appliedAt) > retention {
			delete(s.challengeFinishes, nonce)
		}
	}
	for nonce, startedAt := range s.challengeStarted {
		if now.Sub(startedAt) > retention {
			delete(s.challengeStarted, nonce)
		}
	}
	for nonce, endedAt := range s.streamsEnded {
		if now.Sub(endedAt) > retention {
			delete(s.streamsEnded, nonce)
		}
	}
}

// ChallengeVerifiers returns the host clients that answer VerifyChallenge.
func (s *Session) ChallengeVerifiers() map[int]ChallengeVerifier {
	result := make(map[int]ChallengeVerifier, len(s.clients))
	for i, c := range s.clients {
		if cv, ok := c.(ChallengeVerifier); ok {
			result[i] = cv
		}
	}
	return result
}

// challengeAskDeadline is how long the gateway keeps asking hosts that
// answer pending: the hosts' fetch window plus a slack.
func (s *Session) challengeAskDeadline(now time.Time) time.Time {
	return now.Add(types.ChallengeWindow(s.sm.Config().InferenceSealGraceSeconds) + ChallengeVoteSlack)
}

// RunChallenge collects the verdicts of every host other than the executor
// and sends the signed votes as MsgChallenge in a diff of its own. A record
// that is no longer Finished, or is already challenged, is left alone, and
// so is a session on a protocol before MsgChallenge: its hosts do not know
// the tx, and a diff carrying one would fail on them whole. At the active
// nonce cap, or in a finalize round, no diff could carry the tx either, so
// nothing is asked, as the timeout votes skip there; the Finish stays paid
// the way a timeout past the cap stays unvoted.
func (s *Session) RunChallenge(ctx context.Context, nonce uint64, reason types.ChallengeReason) (ChallengeResult, error) {
	result := ChallengeResult{Nonce: nonce, Reason: reason, ExecutorIdx: -1, Threshold: s.sm.VoteThreshold()}
	rec, ok := s.sm.Inference(nonce)
	if !ok {
		return result, nil
	}
	result.ExecutorIdx = int(rec.ExecutorSlot)
	if rec.Status != types.StatusFinished || rec.UsageChallenged {
		return result, nil
	}
	if version := s.sm.ProtocolVersion(); !types.ProtocolKnowsChallenge(version) {
		result.UnsupportedProtocol = true
		logging.Info("challenge_skipped", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
			"reason", types.ChallengeReasonLabel(reason), "session_protocol", version, "binary_protocol", types.EffectiveStateRootAndProtocolVersion)
		return result, nil
	}
	if s.nonceCapReached() {
		result.NonceCap = true
		logging.Info("challenge_skipped", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
			"reason", types.ChallengeReasonLabel(reason), "cause", "nonce_cap_reached")
		return result, nil
	}
	responseHash := append([]byte(nil), rec.ResponseHash...)
	executorAddr := s.sm.SlotAddress(rec.ExecutorSlot)
	votes, unanswered, unansweredWeight, _ := s.CollectChallengeVotes(ctx, nonce, reason, responseHash, executorAddr, s.ChallengeVerifiers(), s.challengeAskDeadline(time.Now()))
	result.Votes = votes
	result.Unanswered = unanswered
	result.UnansweredWeight = unansweredWeight
	result.Faults = s.challengeFaultWeight(votes)
	if ctx.Err() != nil {
		// Cancelled, not settled: a MsgChallenge below the threshold would
		// mark the record challenged for good while the hosts still fetching
		// could have invalidated it.
		return result, fmt.Errorf("collect challenge votes: %w", ctx.Err())
	}
	if len(votes) == 0 {
		return result, nil
	}
	diff, sendErr := s.sendPendingDiff(ctx, []*types.DevshardTx{challengeTx(nonce, votes)}, nil)
	if errors.Is(sendErr, types.ErrNonceLimitExceeded) {
		// The cap arrived while the votes were collected: no diff can carry
		// the tx, as none could have had the cap been there before the asks.
		result.NonceCap = true
		logging.Info("challenge_skipped", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
			"reason", types.ChallengeReasonLabel(reason), "cause", "nonce_cap_reached", "votes", len(votes))
		return result, nil
	}
	applied := diffCarriesChallenge(diff, nonce)
	after, ok := s.sm.Inference(nonce)
	switch {
	case ok && after.UsageChallenged:
		result.Invalidated = after.Status == types.StatusInvalidated
	case !ok && applied:
		// The record left the live map in the challenge's own diff: the
		// verdict made it terminal, and the auto-seal takes a terminal record
		// as soon as its nonce gate clears, in the same apply. The sealed
		// record keeps the status; without one, the vote weight the state
		// machine tallied says the same.
		if sealed, found := s.sm.LookupSealedInference(nonce); found {
			result.Invalidated = sealed.Status == types.StatusInvalidated
		} else {
			result.Invalidated = result.Faults > result.Threshold
		}
	default:
		if sendErr != nil {
			return result, fmt.Errorf("send challenge diff: %w", sendErr)
		}
		if !ok || after.Status != types.StatusFinished {
			// A replay vote moved the record while the votes were collected,
			// and a terminal record may already be sealed; the state machine
			// refuses a challenge on it, as designed.
			result.ReplayFirst = true
			return result, nil
		}
		return result, fmt.Errorf("inference %d challenge not applied", nonce)
	}
	if sendErr != nil {
		return result, fmt.Errorf("send challenge diff: %w", sendErr)
	}
	return result, nil
}

// diffCarriesChallenge reports whether the diff holds the MsgChallenge of the
// inference: best-effort composition leaves out a tx the state machine refused.
func diffCarriesChallenge(diff types.Diff, inferenceID uint64) bool {
	for _, tx := range diff.Txs {
		if c := tx.GetChallenge(); c != nil && c.InferenceId == inferenceID {
			return true
		}
	}
	return false
}

func challengeTx(inferenceID uint64, votes []*types.ChallengeVote) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Challenge{Challenge: &types.MsgChallenge{
		InferenceId: inferenceID,
		Votes:       votes,
	}}}
}

// CollectChallengeVotes asks each host address other than executorAddr
// once, and again while it answers pending, until deadline. Only votes whose
// signature recovers to the answering host are returned: one bad vote would
// make the state machine reject the whole MsgChallenge. The collection ends
// early once the outcome is settled, as the timeout and error-miss
// collections end at their threshold: when the faults in hand already pass
// the threshold, or when the hosts still to answer could not lift them past
// it. Hosts not awaited count as unanswered, with their slot weight.
func (s *Session) CollectChallengeVotes(
	ctx context.Context,
	nonce uint64,
	reason types.ChallengeReason,
	responseHash []byte,
	executorAddr string,
	verifiers map[int]ChallengeVerifier,
	deadline time.Time,
) (votes []*types.ChallengeVote, unanswered int, unansweredWeight uint32, rejectCauses []string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type addrVerifier struct {
		idx      int
		verifier ChallengeVerifier
		addr     string
	}
	seen := map[string]bool{executorAddr: true}
	var asked []addrVerifier
	var pendingWeight uint32
	for idx, v := range verifiers {
		addr := s.group[idx].ValidatorAddress
		if seen[addr] {
			continue
		}
		seen[addr] = true
		asked = append(asked, addrVerifier{idx: idx, verifier: v, addr: addr})
		pendingWeight += s.sm.AddressSlotCount(addr)
	}
	threshold := s.sm.VoteThreshold()

	type answer struct {
		vote        *types.ChallengeVote
		rejectCause string
		err         error
		addr        string
	}
	results := make(chan answer, len(asked))
	for _, av := range asked {
		go func(av addrVerifier) {
			resp, err := s.askChallengeUntilAnswered(ctx, av.idx, av.addr, av.verifier, nonce, reason, deadline)
			if err != nil {
				results <- answer{err: err, addr: av.addr}
				return
			}
			if len(resp.Signature) == 0 {
				results <- answer{rejectCause: cutChallengeRejectCause(resp.RejectCause), addr: av.addr}
				return
			}
			vote := &types.ChallengeVote{
				VoterSlot: resp.VoterSlot,
				Fault:     resp.Fault,
				Check:     resp.Check,
				Deviation: resp.Deviation,
				Signature: resp.Signature,
			}
			if err := s.verifyChallengeVote(nonce, responseHash, vote, av.addr); err != nil {
				results <- answer{err: err, addr: av.addr}
				return
			}
			results <- answer{vote: vote, addr: av.addr}
		}(av)
	}
	var faultWeight uint32
	for range asked {
		res := <-results
		weight := s.sm.AddressSlotCount(res.addr)
		pendingWeight -= weight
		switch {
		case res.err != nil:
			unanswered++
			unansweredWeight += weight
			logging.Info("challenge_vote_failed", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
				"host", shortAddress(res.addr), "error", res.err)
		case res.vote == nil:
			unanswered++
			unansweredWeight += weight
			if res.rejectCause != "" {
				rejectCauses = append(rejectCauses, res.rejectCause)
			}
			logging.Info("challenge_vote_rejected", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
				"host", shortAddress(res.addr), "reject_cause", res.rejectCause)
		default:
			votes = append(votes, res.vote)
			if res.vote.Fault != types.ChallengeFault_CHALLENGE_FAULT_NONE {
				faultWeight += weight
			}
			logging.Info("challenge_vote", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
				"host", shortAddress(res.addr), "fault", types.ChallengeFaultLabel(res.vote.Fault))
		}
		if ctx.Err() == nil && (faultWeight > threshold || faultWeight+pendingWeight <= threshold) {
			// Settled: the asks still out are cancelled and drain as errors.
			cancel()
		}
	}
	return votes, unanswered, unansweredWeight, rejectCauses
}

var errChallengeStillPending = errors.New("host still fetching at the gateway deadline")

// maxChallengeRejectCause is how much of a voter's reject cause the gateway
// keeps: the cause is the voter's text and the answer may carry 10 MiB.
const maxChallengeRejectCause = 200

func cutChallengeRejectCause(cause string) string {
	if len(cause) <= maxChallengeRejectCause {
		return cause
	}
	return strings.ToValidUTF8(cause[:maxChallengeRejectCause], "") + "…"
}

func (s *Session) askChallengeUntilAnswered(
	ctx context.Context,
	idx int,
	addr string,
	verifier ChallengeVerifier,
	nonce uint64,
	reason types.ChallengeReason,
	deadline time.Time,
) (*transport.VerifyChallengeResponse, error) {
	var lastErr error
	for {
		resp, err := s.askChallengeOnce(ctx, idx, addr, verifier, nonce, reason)
		switch {
		case err == nil && !resp.Pending:
			return resp, nil
		case err == nil:
			lastErr = errChallengeStillPending
		case connect.CodeOf(err) == connect.CodeUnimplemented, connect.CodeOf(err) == connect.CodeInvalidArgument:
			// The host's devshardd predates VerifyChallenge, as on an escrow
			// of the previous protocol, or does not take this request as
			// sent: neither changes inside the window, and asking again
			// would keep the escrow from settling until the deadline.
			return nil, err
		default:
			lastErr = err
		}
		interval := ChallengeReaskInterval
		switch code := connect.CodeOf(err); {
		case lastErr == errChallengeStillPending:
			interval = ChallengePendingReaskInterval
		case code == connect.CodeResourceExhausted, code == connect.CodeUnavailable:
			// The host, or its proxy, is full: it shares the verifier queue
			// with the timeout and error-miss votes, so it is asked again
			// after its Retry-After, and not before the pending interval.
			interval = max(ChallengePendingReaskInterval, transport.ConnectRetryAfter(err))
		}
		if !time.Now().Add(interval).Before(deadline) {
			return nil, lastErr
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Session) askChallengeOnce(
	ctx context.Context,
	idx int,
	addr string,
	verifier ChallengeVerifier,
	nonce uint64,
	reason types.ChallengeReason,
) (*transport.VerifyChallengeResponse, error) {
	waitCtx, waitCancel := context.WithTimeout(ctx, VerifierQueueWaitTimeout)
	err := s.verifierQueue.acquire(waitCtx, addr)
	waitCancel()
	if err != nil {
		return nil, err
	}
	defer s.verifierQueue.release(addr)
	catchUp, err := s.catchUpDiffsForVerifier(ctx, idx)
	if err != nil {
		return nil, fmt.Errorf("catch up verifier: %w", err)
	}
	resp, err := verifier.VerifyChallenge(ctx, nonce, reason, catchUp)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("empty challenge answer")
	}
	return resp, nil
}

func (s *Session) verifyChallengeVote(nonce uint64, responseHash []byte, vote *types.ChallengeVote, expectedAddr string) error {
	if _, known := types.ChallengeFault_name[int32(vote.Fault)]; !known {
		return fmt.Errorf("%w: fault %d", types.ErrUnknownChallengeFault, vote.Fault)
	}
	data, err := types.CanonicalSignedBytes(&types.ChallengeVoteContent{
		EscrowId:     s.escrowID,
		InferenceId:  nonce,
		Fault:        vote.Fault,
		Check:        vote.Check,
		Deviation:    vote.Deviation,
		ResponseHash: responseHash,
	})
	if err != nil {
		return fmt.Errorf("marshal challenge vote content: %w", err)
	}
	recovered, err := s.verifier.RecoverAddress(data, vote.Signature)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidVoteSig, err)
	}
	if recovered != expectedAddr && !s.sm.HostSignerAllowed(vote.VoterSlot, recovered) {
		return fmt.Errorf("%w: expected %s, got %s", types.ErrInvalidVoteSig, expectedAddr, recovered)
	}
	if owner := s.sm.SlotAddress(vote.VoterSlot); owner != expectedAddr {
		return fmt.Errorf("%w: slot %d is owned by %s, not by responder %s",
			types.ErrInvalidVoteSig, vote.VoterSlot, owner, expectedAddr)
	}
	return nil
}

// challengeFaultWeight is the slot weight of fault votes, one count per
// address, as the state machine tallies it.
func (s *Session) challengeFaultWeight(votes []*types.ChallengeVote) uint32 {
	counted := make(map[string]bool, len(votes))
	var weight uint32
	for _, v := range votes {
		if v.Fault == types.ChallengeFault_CHALLENGE_FAULT_NONE {
			continue
		}
		addr := s.sm.SlotAddress(v.VoterSlot)
		if counted[addr] {
			continue
		}
		counted[addr] = true
		weight += s.sm.AddressSlotCount(addr)
	}
	return weight
}
