//go:build !dev && !debug && !development

package transport

import "context"

func (s *Server) ArmHoldInferenceResponse() {}

func (s *Server) ReleaseHoldInferenceResponse() {}

func (s *Server) waitInferenceResponseHold(context.Context, uint64) error { return nil }
