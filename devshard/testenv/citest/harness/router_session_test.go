package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouterSessionURL_StickyPath(t *testing.T) {
	got := RouterSessionURL("http://127.0.0.1:18080", "v2", "escrow-42", "/healthz")
	require.Equal(t, "http://127.0.0.1:18080/v2/sessions/escrow-42/healthz", got)
}
