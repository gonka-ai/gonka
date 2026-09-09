//go:build !dev

package scenarios

import (
	"testing"

	"devshard/transport"
)

func inferenceHoldsEnabled() bool { return false }

func armInferenceResponseHold(_ *testing.T, _ *transport.Server) {}

func releaseInferenceResponseHold(_ *testing.T, _ *transport.Server) {}
