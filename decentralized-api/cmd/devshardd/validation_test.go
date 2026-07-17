package main

import (
	"testing"

	devshardpkg "devshard"

	"github.com/stretchr/testify/require"
)

func TestDevshardValidatorPayloadPathUsesRuntimeVersion(t *testing.T) {
	validator := &devshardValidator{runtimeVersion: "oracle-v2"}

	path := validator.sessionPayloadPath(devshardpkg.ValidateRequest{EscrowID: "escrow-123"})

	require.Equal(t, "devshard/oracle-v2/sessions/escrow-123/payloads", path)
}
