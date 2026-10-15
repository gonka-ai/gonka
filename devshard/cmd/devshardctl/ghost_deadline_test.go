package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGhostProbeParams_StampsStartedAtInSeconds(t *testing.T) {
	params := ghostProbeParams("llama")

	require.InDelta(t, time.Now().Unix(), params.StartedAt, 5, "StartedAt must be Unix seconds")
}
