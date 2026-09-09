package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPayloadFaultMatches(t *testing.T) {
	t.Parallel()
	require.False(t, payloadFaultMatches(0, "", "val-a"))
	require.False(t, payloadFaultMatches(-1, "", "val-a"))
	require.True(t, payloadFaultMatches(500, "", "val-a"))
	require.True(t, payloadFaultMatches(500, "val-a", "val-a"))
	require.False(t, payloadFaultMatches(500, "val-a", "val-b"))
	require.True(t, payloadFaultMatches(404, "val-b", "val-b"))
}

func TestParsePayloadFault(t *testing.T) {
	t.Parallel()
	status, addr := parsePayloadFault("", "")
	require.Equal(t, 0, status)
	require.Empty(t, addr)

	status, addr = parsePayloadFault("500", "gonka1validator")
	require.Equal(t, 500, status)
	require.Equal(t, "gonka1validator", addr)

	status, addr = parsePayloadFault("not-a-number", "gonka1validator")
	require.Equal(t, 0, status)
	require.Empty(t, addr)

	status, _ = parsePayloadFault("0", "")
	require.Equal(t, 0, status)
}

// Production builds must ignore the env entirely: the handler fails payload GETs
// before authentication, so an accidental env var would turn a real executor
// into a payload withholder.
func TestPayloadFaultFromEnv_RespectsBuildTag(t *testing.T) {
	t.Setenv(envTestenvPayloadHTTPStatus, "500")
	t.Setenv(envTestenvPayloadFaultAddr, "gonka1validator")

	status, addr := payloadFaultFromEnv()
	if payloadFaultBuildEnabled {
		require.Equal(t, 500, status)
		require.Equal(t, "gonka1validator", addr)
		return
	}
	require.Equal(t, 0, status)
	require.Empty(t, addr)
}
