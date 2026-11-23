package limits

import "time"

type hostState struct {
	bounds                  ModelWindows
	input                   window
	output                  window
	consecutiveCutoffFaults int
	openUntil               time.Time
	backoffCount            int
	halfOpen                bool
	openedAt                time.Time
	cutoffGeneration        uint64
	probeInFlight           bool
	lastUsed                time.Time
}

type probeTicket struct {
	isProbe    bool
	generation uint64
}

func (s *hostState) idle() bool {
	return s.input.inflight == 0 && s.output.inflight == 0
}

func (s *hostState) clearBreaker() {
	s.consecutiveCutoffFaults = 0
	s.openUntil = time.Time{}
	s.backoffCount = 0
	s.halfOpen = false
}

func (s *hostState) recoverBreaker() (lifted bool) {
	s.consecutiveCutoffFaults = 0
	if !s.halfOpen {
		return false
	}
	s.halfOpen = false
	s.openUntil = time.Time{}
	if s.backoffCount > 0 {
		s.backoffCount--
	}
	return true
}

func (s *hostState) probeIfCutOff() probeTicket {
	if s.openUntil.IsZero() {
		return probeTicket{}
	}
	s.halfOpen = true
	s.probeInFlight = true
	return probeTicket{isProbe: true, generation: s.cutoffGeneration}
}

func (s *hostState) endProbe(ticket probeTicket) {
	if ticket.isProbe && ticket.generation == s.cutoffGeneration {
		s.probeInFlight = false
	}
}

func (s *hostState) recordCutoffFault(now time.Time, afterFailures int) (shouldOpen bool) {
	if now.Before(s.openUntil) {
		return false
	}
	s.consecutiveCutoffFaults++
	return s.consecutiveCutoffFaults >= afterFailures || s.halfOpen
}

func (s *hostState) openBreaker(now, until time.Time, backoffGrows bool) (reason string) {
	s.openedAt = now
	s.cutoffGeneration++
	s.probeInFlight = false
	s.openUntil = until
	if backoffGrows {
		s.backoffCount++
	}
	reason = cutoffReason(s.halfOpen)
	s.halfOpen = false
	s.consecutiveCutoffFaults = 0
	return reason
}
