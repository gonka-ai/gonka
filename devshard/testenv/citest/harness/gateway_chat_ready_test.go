package harness

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/transport"
)

func TestGatewayChatCatalogNotReady(t *testing.T) {
	header := http.Header{}
	header.Set(transport.HeaderDevshardError, transport.DevshardErrorUndeclaredVersion)
	require.True(t, gatewayChatCatalogNotReady(http.StatusServiceUnavailable, header, []byte("busy")),
		"X-Devshard-Error: undeclared_version on 503 is not-ready")

	router := http.Header{}
	router.Set(transport.HeaderDevshardRouterError, transport.DevshardErrorUndeclaredVersion)
	require.True(t, gatewayChatCatalogNotReady(http.StatusServiceUnavailable, router, nil),
		"X-Devshard-Router-Error: undeclared_version on 503 is not-ready")

	bodyOnly := http.Header{}
	require.True(t, gatewayChatCatalogNotReady(http.StatusServiceUnavailable, bodyOnly,
		[]byte("version v2 is not present in the governance routing catalog")))

	require.False(t, gatewayChatCatalogNotReady(http.StatusTooManyRequests, header, nil),
		"429 must end the wait even with the undeclared_version header")
	require.False(t, gatewayChatCatalogNotReady(http.StatusOK, header, nil))
	require.False(t, gatewayChatCatalogNotReady(http.StatusServiceUnavailable, http.Header{}, []byte("nginx limit")),
		"host 503 without catalog markers must end the wait")
}
