package chain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoundedCometRPCNodeFailsWhenEndpointNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	node, err := newBoundedCometRPCNode(srv.URL, 1)
	require.NoError(t, err)

	start := time.Now()
	_, err = node.BroadcastTxSync(context.Background(), []byte("tx"))
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
}
