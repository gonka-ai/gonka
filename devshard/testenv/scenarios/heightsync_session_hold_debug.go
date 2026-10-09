//go:build dev

package scenarios

import (
	"testing"

	"devshard/transport"
)

func inferenceHoldsEnabled() bool { return true }

func armInferenceResponseHold(t *testing.T, srv *transport.Server) {
	t.Helper()
	srv.ArmHoldInferenceResponse()
}

func releaseInferenceResponseHold(t *testing.T, srv *transport.Server) {
	t.Helper()
	srv.ReleaseHoldInferenceResponse()
}
