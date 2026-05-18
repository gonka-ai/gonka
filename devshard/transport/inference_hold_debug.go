//go:build dev || debug || development

package transport

import (
	"context"

	"devshard/logging"
)

// ArmHoldInferenceResponse blocks the next HandleInference after the host has
// processed the diff (Confirm is ready) but before any SSE bytes are written.
// Used by testenv lost-first-response scenarios. One-shot per arm.
func (s *Server) ArmHoldInferenceResponse() {
	s.holdInferenceMu.Lock()
	defer s.holdInferenceMu.Unlock()
	s.holdInferenceGate = make(chan struct{})
	s.holdInferenceArmed = true
}

// ReleaseHoldInferenceResponse unblocks a held inference response.
func (s *Server) ReleaseHoldInferenceResponse() {
	s.holdInferenceMu.Lock()
	defer s.holdInferenceMu.Unlock()
	if s.holdInferenceGate != nil {
		close(s.holdInferenceGate)
		s.holdInferenceGate = nil
	}
	s.holdInferenceArmed = false
}

func (s *Server) waitInferenceResponseHold(ctx context.Context, inferenceNonce uint64) error {
	s.holdInferenceMu.Lock()
	armed := s.holdInferenceArmed
	gate := s.holdInferenceGate
	s.holdInferenceMu.Unlock()
	if !armed || gate == nil {
		return nil
	}
	logging.Debug("debug: holding inference HTTP response before SSE",
		"subsystem", "transport", "nonce", inferenceNonce)
	select {
	case <-gate:
		s.holdInferenceMu.Lock()
		s.holdInferenceArmed = false
		s.holdInferenceGate = nil
		s.holdInferenceMu.Unlock()
		return nil
	case <-ctx.Done():
		s.holdInferenceMu.Lock()
		s.holdInferenceArmed = false
		s.holdInferenceGate = nil
		s.holdInferenceMu.Unlock()
		return ctx.Err()
	}
}
