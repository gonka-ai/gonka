package user

import (
	"bytes"
	"time"

	"devshard/logging"
	"devshard/types"
)

const (
	ServedBindingBound    ServedBinding = "bound"
	ServedBindingMismatch ServedBinding = "mismatch"
)

type ServedBinding string

type ServedBindingHandler func(nonce uint64, hostIndex int, participantKey string, verdict ServedBinding)

type servedFinishHashes struct {
	responseHash []byte
	servedHash   []byte
}

type waitingReceivedStream struct {
	hashes     [][32]byte
	receivedAt time.Time
}

type waitingAppliedFinish struct {
	hashes    servedFinishHashes
	appliedAt time.Time
}

func (s *Session) SetServedBindingHandler(handler ServedBindingHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.servedBindingHandler = handler
}

func (s *Session) BindReceivedStream(nonce uint64, received [][32]byte) {
	if len(received) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streamsOpen, nonce)
	finish, isApplied := s.appliedFinishHashes[nonce]
	if !isApplied {
		if s.receivedStreams == nil {
			s.receivedStreams = make(map[uint64]waitingReceivedStream)
		}
		now := s.nowLocked()
		s.receivedStreams[nonce] = waitingReceivedStream{hashes: received, receivedAt: now}
		s.forgetExpiredServedBindingsLocked(now)
		return
	}
	delete(s.appliedFinishHashes, nonce)
	s.reportServedBindingLocked(nonce, judgeServedBinding(received, finish.hashes))
}

func (s *Session) bindAppliedFinishLocked(finish *types.MsgFinishInference) {
	nonce := finish.InferenceId
	challenged := s.challengeFinishAppliedLocked(nonce)
	hashes := servedFinishHashes{responseHash: finish.ResponseHash, servedHash: finish.ServedHash}
	stream, isReceived := s.receivedStreams[nonce]
	if !isReceived {
		if challenged {
			return
		}
		if s.appliedFinishHashes == nil {
			s.appliedFinishHashes = make(map[uint64]waitingAppliedFinish)
		}
		now := s.nowLocked()
		s.appliedFinishHashes[nonce] = waitingAppliedFinish{hashes: hashes, appliedAt: now}
		s.forgetExpiredServedBindingsLocked(now)
		return
	}
	delete(s.receivedStreams, nonce)
	s.reportServedBindingLocked(nonce, judgeServedBinding(stream.hashes, hashes))
}

func (s *Session) reportServedBindingLocked(nonce uint64, verdict ServedBinding) {
	delete(s.challengeFinishes, nonce)
	if verdict == ServedBindingMismatch {
		s.startChallengeLocked(nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	}
	if s.servedBindingHandler == nil || len(s.group) == 0 {
		return
	}
	hostIndex := int(nonce % uint64(len(s.group)))
	s.servedBindingHandler(nonce, hostIndex, s.hostParticipantKeyLocked(hostIndex), verdict)
}

func (s *Session) forgetExpiredServedBindingsLocked(now time.Time) {
	var executionTimeoutSeconds int64
	if s.sm != nil {
		executionTimeoutSeconds = s.sm.Config().ExecutionTimeout
	}
	retention := servedBindingRetention(executionTimeoutSeconds)
	if now.Sub(s.servedBindingsPrunedAt) < retention {
		return
	}
	s.servedBindingsPrunedAt = now
	for nonce, stream := range s.receivedStreams {
		if now.Sub(stream.receivedAt) > retention && !s.finishCanStillApplyLocked(nonce) {
			delete(s.receivedStreams, nonce)
		}
	}
	for nonce, finish := range s.appliedFinishHashes {
		if now.Sub(finish.appliedAt) > retention {
			delete(s.appliedFinishHashes, nonce)
			if _, ended := s.streamsEnded[nonce]; ended {
				// A probe, a phase-change abort or a cut before any receipt:
				// its Finish was never going to be checked.
				continue
			}
			logging.Info("applied Finish with no stream to check against, unchecked", "subsystem", "challenge",
				"escrow", s.escrowID, "nonce", nonce)
		}
	}
	s.forgetExpiredChallengesLocked(now, retention)
}

func (s *Session) finishCanStillApplyLocked(nonce uint64) bool {
	if s.sm == nil {
		return false
	}
	record, isTracked := s.sm.Inference(nonce)
	return isTracked && (record.Status == types.StatusPending || record.Status == types.StatusStarted)
}

func servedBindingRetention(executionTimeoutSeconds int64) time.Duration {
	return 2 * (time.Duration(executionTimeoutSeconds)*time.Second + TimeoutBuffer)
}

func judgeServedBinding(received [][32]byte, finish servedFinishHashes) ServedBinding {
	for _, sum := range received {
		if bytes.Equal(sum[:], finish.responseHash) || bytes.Equal(sum[:], finish.servedHash) {
			return ServedBindingBound
		}
	}
	return ServedBindingMismatch
}
