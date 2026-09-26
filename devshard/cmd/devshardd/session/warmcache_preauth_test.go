package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"common/chain"
	"common/utils"
	chainbridge "devshard/cmd/devshardd/bridge"
	"devshard/observability"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/types"
)

// The payload route resolves X-Validator-Address against the group before it
// checks the signature (authenticatePayloadRequest is called directly here),
// so unsigned requests with fresh addresses must not grow the warm-key cache
// without bound. Eviction is observed through the chain: after a flood larger
// than the cache, the earliest addresses cost queries again.
func TestPayloadAuth_UnsignedRandomWarmAddrsDoNotGrowCacheUnbounded(t *testing.T) {
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: seed.Defaults()})
	require.NoError(t, err)
	t.Cleanup(func() { srv.Stop(); _ = lis.Close() })

	var queries atomic.Int64
	count := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if method == "/inference.inference.Query/GranteesByMessageType" {
			queries.Add(1)
		}
		return inv(ctx, method, req, reply, cc, opts...)
	}
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(count))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	m := &HostManager{bridge: chainbridge.NewChainBridge(chain.NewFromConn(conn), nil)}
	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "gonka1slot0000000000000000000000000000000"},
		{SlotID: 1, ValidatorAddress: "gonka1slot1000000000000000000000000000000"},
		{SlotID: 2, ValidatorAddress: "gonka1slot2000000000000000000000000000000"},
	}

	const n = 2000 // 6000 negatives on a 3-slot group
	send := func(i int) observability.Reason {
		req := httptest.NewRequest(http.MethodGet, "/v1/sessions/1/payloads?inference_id=x", nil)
		req.Header.Set(utils.XValidatorAddressHeader, fmt.Sprintf("attacker-%d", i))
		req.Header.Set(utils.XTimestampHeader, strconv.FormatInt(time.Now().UnixNano(), 10))
		req.Header.Set(utils.XEpochIdHeader, "1")
		req.Header.Set(utils.AuthorizationHeader, "not-a-signature")
		_, reason, err := m.authenticatePayloadRequest(echo.New().NewContext(req, httptest.NewRecorder()), group)
		require.Error(t, err)
		return reason
	}

	for i := 0; i < n; i++ {
		require.Equal(t, observability.ReasonNotGroupMember, send(i))
	}
	t.Logf("first pass: %d unsigned requests -> %d chain queries", n, queries.Load())
	require.Equal(t, int64(n*len(group)), queries.Load())

	before := queries.Load()
	const replay = 200
	for i := 0; i < replay; i++ {
		send(i)
	}
	t.Logf("replay of the first %d: %d new chain queries", replay, queries.Load()-before)
	require.Equal(t, int64(replay*len(group)), queries.Load()-before,
		"the earliest negatives were evicted, not kept for the life of the process")
}
