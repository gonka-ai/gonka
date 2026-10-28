package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"devshard/transport"
	"devshard/user"
)

func (s *attemptState) classify(ctx context.Context, spec AttemptSpec, err error) {
	defer s.classifyPrefixCancel(ctx)
	if err != nil {
		s.lifecycle.EscrowMissing = transport.IsUpstreamEscrowNotFound(err)
		s.stateDivergent = errors.Is(err, ErrStateRootDivergence)
		s.hostAbsent = errors.Is(err, user.ErrHostAbsentFromChain)
		s.localFault = errors.Is(err, ErrLocalRootUnavailable) || s.hostAbsent
		s.upstreamStatus, s.upstreamBody = upstreamRefusal(err)
		s.terminal = classifyDispatchError(ctx, err)
		s.capability = capabilityOfDispatchError(err)
		return
	}
	s.record(spec.Classifier.Flush())

	switch {
	case s.receiptTime.IsZero():
		s.terminal = TerminalNoReceipt
	case s.errorSource != "" && s.capabilityRefused:
		s.terminal = TerminalCapabilityRefused
	case s.errorSource != "":
		s.terminal = TerminalErrorStream
	case s.contentChunks > 0:
		s.terminal = TerminalLost
	case s.tokensBurned:
		s.terminal = TerminalBurnEmpty
	default:
		s.terminal = TerminalEmptyStream
	}
}

func (s *attemptState) classifyPrefixCancel(ctx context.Context) {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errPrefixGatewayPressure):
		s.gatewayPressure = true
		s.terminal = TerminalResponseTooLarge
	case errors.Is(cause, errPrefixOverBudget):
		s.terminal = TerminalResponseTooLarge
	}
}

// readEmpty reports an answer the gateway read as carrying nothing. See race.md, "Reading an empty answer back".
func (s *attemptState) readEmpty() bool {
	return s.terminal == TerminalEmptyStream || s.terminal == TerminalBurnEmpty
}

// emptyChunkHeads are offered only where they answer something.
func (s *attemptState) emptyChunkHeads() (first, last string) {
	if !s.readEmpty() {
		return "", ""
	}
	if s.firstChunkHead == s.lastChunkHead {
		return "", s.lastChunkHead
	}
	return s.firstChunkHead, s.lastChunkHead
}

// upstreamRefusal keeps what the host said when it refused, truncated: a log line needs the reason, not the payload.
func upstreamRefusal(err error) (int, string) {
	status, refused := hostRefusal(err)
	if !refused {
		return 0, ""
	}
	body := strings.TrimSpace(status.Body)
	if len(body) > maxUpstreamBodyLogged {
		body = body[:maxUpstreamBodyLogged]
	}
	return status.StatusCode, body
}

func classifyDispatchError(ctx context.Context, err error) Terminal {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return TerminalClientCancelled
	}

	if transport.IsEscrowLookupLimitedError(err) {
		return TerminalEscrowLookupLimited
	}
	if status, refused := hostRefusal(err); refused {
		if !strings.Contains(status.Path, "/chat/completions") {
			return TerminalOffPath
		}
		if status.StatusCode == http.StatusUnauthorized &&
			!strings.Contains(strings.ToLower(status.Body), "timestamp drift") {
			return TerminalRejected
		}
		if terminal, recovered := terminalForStatus[status.StatusCode]; recovered {
			return terminal
		}
		if status.StatusCode >= http.StatusInternalServerError && !transport.IsUpstreamRequestFault(status) {
			return TerminalUpstreamServerError
		}
		return TerminalRejected
	}

	switch {
	case errors.Is(err, transport.ErrHostRequestTooLarge):
		return TerminalRequestTooLarge
	case errors.Is(err, transport.ErrSSEEventTooLarge), errors.Is(err, transport.ErrResponseBodyTooLarge):
		return TerminalResponseTooLarge
	case errors.Is(err, transport.ErrSSEStreamTruncated):
		return TerminalStreamTruncated
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return TerminalUnexpectedEOF
	}
	return TerminalDialFailure
}

func (s *attemptState) outcome(spec AttemptSpec) *AttemptOutcome {
	firstHead, lastHead := s.emptyChunkHeads()
	return &AttemptOutcome{
		Participant: spec.Participant,
		HostIdx:     spec.HostIdx,
		HostLabel:   spec.HostLabel,
		Nonce:       spec.Nonce.Nonce(),
		Role:        spec.Role,
		StartReason: spec.StartReason,
		Suspicious:  spec.Suspicious,

		SendTime:     s.sendTime,
		ReceiptTime:  s.receiptTime,
		FirstToken:   s.firstToken,
		FirstContent: s.firstContent,
		LastChunk:    s.lastChunk,
		Completed:    s.completed,

		ContentChunks:         s.contentChunks,
		StreamChunks:          s.streamChunks,
		UsageCompletionTokens: s.usageCompletionTokens,
		UsagePromptTokens:     s.usagePromptTokens,
		LogprobTokens:         s.logprobTokens,
		OutputBytes:           s.outputBytes,
		LogprobsDecoded:       s.logprobsDecoded,
		HostTerminated:        s.hostTerminated,
		MaxChunkGap:           s.maxChunkGap,
		MaxChunkGapAt:         s.maxGapChunk,
		MeanChunkGap:          s.meanChunkGap(),
		DroppedEvents:         s.droppedEvents,

		Terminal:    s.terminal,
		Confirmed:   s.confirmed,
		ConfirmedAt: s.confirmedAt,

		UpstreamStatus: s.upstreamStatus,
		UpstreamBody:   s.upstreamBody,
		FirstChunkHead: firstHead,
		LastChunkHead:  lastHead,
		FinishReason:   s.finishReason,

		ContentSource: s.contentSource,
		Capability:    s.capability,
		ErrorSource:   s.errorSource,
		ErrorCode:     s.errorCode,
		ErrorType:     s.errorType,
		ErrorMessage:  s.errorMessage,
		ErrorPayload:  s.errorPayload,
		MissProof:     s.missProof(spec),

		StateDivergent:  s.stateDivergent,
		LocalFault:      s.localFault,
		HostAbsent:      s.hostAbsent,
		GatewayPressure: s.gatewayPressure,
	}
}

func (s *attemptState) missProof(spec AttemptSpec) *MissProof {
	prover, holds := spec.Classifier.(missProver)
	if !holds {
		return nil
	}
	if s.terminal != TerminalErrorStream {
		prover.releaseMissProof()
		return nil
	}
	proof, held := prover.missProof()
	if !held {
		return nil
	}
	return &proof
}
