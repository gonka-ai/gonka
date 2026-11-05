package user

import (
	"bytes"
	"time"

	"devshard/logging"
	"devshard/types"
)

// ServedText is what the gateway read of a stream past its hashes: the text of the answer's first
// choice and what the request lets the answer spend past that text. The zero value is a stream the
// gateway does not judge, a probe's.
type ServedText struct {
	// Measured marks a stream whose text the gateway counted.
	Measured bool
	// Unmeasured marks a stream the gateway relayed but lost count of: its text outgrew the scan
	// buffers. Its claim cannot be held to the byte bounds, so it is challenged for the voters to count.
	Unmeasured bool
	// Bytes are the UTF-8 bytes of content, reasoning and tool call names and arguments.
	Bytes uint64
	// ToolCalls is how many tool calls the answer made, and Arguments how many top-level
	// arguments they carried.
	ToolCalls uint64
	Arguments uint64
	// StopBytes is the longest stop string of the request: its tokens are generated and cut.
	StopBytes uint64
	// Streamed is true when the ML node was asked to stream, so the client got the answer as deltas.
	Streamed bool
	// TextOnly is false for a request with content parts that are not text.
	TextOnly bool
}

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
	text       ServedText
	receivedAt time.Time
}

type waitingAppliedFinish struct {
	hashes       servedFinishHashes
	inputTokens  uint64
	outputTokens uint64
	appliedAt    time.Time
}

func (s *Session) SetServedBindingHandler(handler ServedBindingHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.servedBindingHandler = handler
}

func (s *Session) BindReceivedStream(nonce uint64, received [][32]byte) {
	s.BindServedStream(nonce, received, ServedText{})
}

// BindServedStream hands the hashes of a full received stream to the served binding, with the text
// the gateway counted on it. A bound stream whose Finish claims more tokens than the text bounds
// is challenged.
func (s *Session) BindServedStream(nonce uint64, received [][32]byte, text ServedText) {
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
		s.receivedStreams[nonce] = waitingReceivedStream{hashes: received, text: text, receivedAt: now}
		s.forgetExpiredServedBindingsLocked(now)
		return
	}
	delete(s.appliedFinishHashes, nonce)
	verdict := judgeServedBinding(received, finish.hashes)
	s.reportServedBindingLocked(nonce, verdict)
	if verdict == ServedBindingBound {
		s.judgeServedUsageLocked(nonce, text, finish.inputTokens, finish.outputTokens)
	}
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
		s.appliedFinishHashes[nonce] = waitingAppliedFinish{hashes: hashes, inputTokens: finish.InputTokens,
			outputTokens: finish.OutputTokens, appliedAt: now}
		s.forgetExpiredServedBindingsLocked(now)
		return
	}
	delete(s.receivedStreams, nonce)
	verdict := judgeServedBinding(stream.hashes, hashes)
	s.reportServedBindingLocked(nonce, verdict)
	if verdict == ServedBindingBound && !challenged {
		s.judgeServedUsageLocked(nonce, stream.text, finish.InputTokens, finish.OutputTokens)
	}
}

// judgeServedUsageLocked challenges the claim of a bound stream that is above what its text and
// its request allow: output tokens above the byte bound (OUTPUT_ABOVE_BYTES), input tokens above
// twice the request's bytes (INPUT_ABOVE_BYTES). Only a streamed answer has its output judged and
// only a text-only request its input; a stream whose text was not counted is challenged as
// UNMEASURED. At most one challenge starts for an inference.
func (s *Session) judgeServedUsageLocked(nonce uint64, text ServedText, inputTokens, outputTokens uint64) {
	if !(text.Measured || text.Unmeasured) || s.sm == nil {
		return
	}
	record, ok := s.sm.Inference(nonce)
	if !ok {
		return
	}
	if text.Unmeasured {
		logging.Info("claim of a stream the gateway did not count", "subsystem", "challenge", "escrow", s.escrowID,
			"nonce", nonce, "reason", types.ChallengeReasonLabel(types.ChallengeReason_CHALLENGE_REASON_UNMEASURED),
			"model", record.Model, "output_tokens", outputTokens, "input_tokens", inputTokens, "max_tokens", record.MaxTokens)
		s.startChallengeLocked(nonce, types.ChallengeReason_CHALLENGE_REASON_UNMEASURED)
		return
	}
	outputBound := types.OutputByteBound(record.Model, text.Bytes, text.StopBytes, text.ToolCalls, text.Arguments, record.MaxTokens)
	inputBound := types.InputByteBound(record.Model, record.InputLength)
	reason := types.ChallengeReason_CHALLENGE_REASON_UNSPECIFIED
	switch {
	case text.Streamed && outputBound > 0 && outputTokens > outputBound:
		reason = types.ChallengeReason_CHALLENGE_REASON_OUTPUT_ABOVE_BYTES
	case text.TextOnly && inputBound > 0 && inputTokens > inputBound:
		reason = types.ChallengeReason_CHALLENGE_REASON_INPUT_ABOVE_BYTES
	default:
		return
	}
	allowance, _ := types.UsageAllowanceFor(record.Model)
	logging.Info("claim above the byte bound", "subsystem", "challenge", "escrow", s.escrowID, "nonce", nonce,
		"reason", types.ChallengeReasonLabel(reason), "model", record.Model,
		"output_tokens", outputTokens, "output_bound", outputBound, "text_bytes", text.Bytes,
		"tool_calls", text.ToolCalls, "arguments", text.Arguments, "stop_bytes", text.StopBytes, "max_tokens", record.MaxTokens,
		"input_tokens", inputTokens, "input_bound", inputBound, "input_length", record.InputLength,
		"service_tokens", allowance.ServiceTokens, "tool_call_header", allowance.ToolCallHeader, "argument_header", allowance.ArgumentHeader,
		"prompt_template", allowance.PromptTemplate)
	s.startChallengeLocked(nonce, reason)
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
