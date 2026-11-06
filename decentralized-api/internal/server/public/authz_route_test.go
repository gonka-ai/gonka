package public

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"common/utils"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal"
	"decentralized-api/internal/authzcache"
	"decentralized-api/poc/artifacts"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type authzRouteQueryServer struct {
	types.UnimplementedQueryServer
	mu      sync.Mutex
	calls   map[string]int
	block   map[string]chan struct{}
	errs    map[string]error
	members []string
}

func newAuthzRouteQueryServer(members ...string) *authzRouteQueryServer {
	return &authzRouteQueryServer{
		calls:   map[string]int{},
		block:   map[string]chan struct{}{},
		errs:    map[string]error{},
		members: members,
	}
}

func (s *authzRouteQueryServer) GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error) {
	s.mu.Lock()
	s.calls[req.GranterAddress]++
	block := s.block[req.GranterAddress]
	err := s.errs[req.GranterAddress]
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &types.QueryGranteesByMessageTypeResponse{}, nil
}

func (s *authzRouteQueryServer) AccountByAddress(_ context.Context, req *types.QueryAccountByAddressRequest) (*types.QueryAccountByAddressResponse, error) {
	return &types.QueryAccountByAddressResponse{Pubkey: base64.StdEncoding.EncodeToString([]byte("pubkey_" + req.Address))}, nil
}

func (s *authzRouteQueryServer) Inference(context.Context, *types.QueryGetInferenceRequest) (*types.QueryGetInferenceResponse, error) {
	return &types.QueryGetInferenceResponse{Inference: types.Inference{EpochId: 1}}, nil
}

func (s *authzRouteQueryServer) EpochGroupData(context.Context, *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
	weights := make([]*types.ValidationWeight, 0, len(s.members))
	for _, member := range s.members {
		weights = append(weights, &types.ValidationWeight{MemberAddress: member})
	}
	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: types.EpochGroupData{ValidationWeights: weights}}, nil
}

func (s *authzRouteQueryServer) callCount(granter string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[granter]
}

type authzRoute struct {
	name string
	call func(t *testing.T, server *Server, granter string) error
}

func serveRoute(t *testing.T, handler func(echo.Context) error, req *http.Request) error {
	t.Helper()
	return handler(echo.New().NewContext(req, httptest.NewRecorder()))
}

func pocRouteRequest(t *testing.T, path string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func pocRouteBase(granter string) (string, string, string, string, StringInt64) {
	return "model-a", base64.StdEncoding.EncodeToString(make([]byte, 32)), granter, base64.StdEncoding.EncodeToString([]byte("sig")), StringInt64(time.Now().UnixNano())
}

var authzRoutes = []authzRoute{
	{
		name: "POST /v1/poc/proofs",
		call: func(t *testing.T, server *Server, granter string) error {
			modelId, rootHash, validator, signature, timestamp := pocRouteBase(granter)
			req := pocRouteRequest(t, "/v1/poc/proofs", &PocProofsRequest{
				PocStageStartBlockHeight: 100,
				ModelId:                  modelId,
				RootHash:                 rootHash,
				Count:                    1,
				LeafIndices:              []StringUint32{0},
				ValidatorAddress:         validator,
				ValidatorSignerAddress:   validator,
				Timestamp:                timestamp,
				Signature:                signature,
			})
			return serveRoute(t, server.postPocProofs, req)
		},
	},
	{
		name: "POST /v1/poc/proofs/by-nonce",
		call: func(t *testing.T, server *Server, granter string) error {
			modelId, rootHash, validator, signature, timestamp := pocRouteBase(granter)
			req := pocRouteRequest(t, "/v1/poc/proofs/by-nonce", &PocProofsByNonceRequest{
				PocStageStartBlockHeight: 100,
				ModelId:                  modelId,
				RootHash:                 rootHash,
				Count:                    1,
				Nonces:                   []StringInt32{10},
				ValidatorAddress:         validator,
				ValidatorSignerAddress:   validator,
				Timestamp:                timestamp,
				Signature:                signature,
			})
			return serveRoute(t, server.postPocProofsByNonce, req)
		},
	},
	{
		name: "GET /v1/inference/payloads",
		call: func(t *testing.T, server *Server, granter string) error {
			req := httptest.NewRequest(http.MethodGet, "/v1/inference/payloads?inference_id=inference-1", nil)
			req.Header.Set(utils.XValidatorAddressHeader, granter)
			req.Header.Set(utils.XTimestampHeader, strconv.FormatInt(time.Now().UnixNano(), 10))
			req.Header.Set(utils.XEpochIdHeader, "1")
			req.Header.Set(utils.AuthorizationHeader, base64.StdEncoding.EncodeToString([]byte("sig")))
			return serveRoute(t, server.getInferencePayloads, req)
		},
	},
}

func newAuthzRouteServer(t *testing.T, chain *authzRouteQueryServer) *Server {
	t.Helper()
	queryClient, cleanup := newProofQueryClient(t, chain)
	t.Cleanup(cleanup)

	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)

	store := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	t.Cleanup(func() { _ = store.Close() })

	return &Server{
		artifactStore:       store,
		recorder:            recorder,
		authzCache:          authzcache.NewAuthzCache(recorder),
		epochGroupDataCache: internal.NewEpochGroupDataCache(recorder),
	}
}

func requireHTTPError(t *testing.T, err error, code int, message string) {
	t.Helper()
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, code, httpErr.Code)
	require.Equal(t, message, httpErr.Message)
}

// Test flow, for each route:
// 1. Block the chain lookup for a valid failing granter A and start a request for A in the background.
// 2. Wait until the lookup for A has reached the chain.
// 3. Send a request for granter B and require it to get past authorization to the signature check while A is blocked.
// 4. Release A with a chain error and require the request for A to be rejected as an unknown validator.
func TestAuthzRoutes_SlowGranterDoesNotBlockOtherGranters(t *testing.T) {
	for _, route := range authzRoutes {
		t.Run(route.name, func(t *testing.T) {
			granterA := sdk.AccAddress([]byte("granterA_20_bytes___")).String()
			granterB := sdk.AccAddress([]byte("granterB_20_bytes___")).String()
			chain := newAuthzRouteQueryServer(granterA, granterB)
			release := make(chan struct{})
			chain.block[granterA] = release
			chain.errs[granterA] = status.Error(codes.Unavailable, "node down")
			server := newAuthzRouteServer(t, chain)

			resultA := make(chan error, 1)
			go func() { resultA <- route.call(t, server, granterA) }()
			require.Eventually(t, func() bool { return chain.callCount(granterA) == 1 }, 5*time.Second, time.Millisecond,
				"lookup for granterA never reached the chain")

			resultB := make(chan error, 1)
			go func() { resultB <- route.call(t, server, granterB) }()
			select {
			case err := <-resultB:
				requireHTTPError(t, err, http.StatusUnauthorized, "invalid signature")
			case <-time.After(5 * time.Second):
				close(release)
				<-resultA
				t.Fatal("request for granterB blocked behind the lookup for granterA")
			}

			close(release)
			select {
			case err := <-resultA:
				requireHTTPError(t, err, http.StatusUnauthorized, "validator not found")
			case <-time.After(5 * time.Second):
				t.Fatal("request for granterA did not return after its chain lookup was released")
			}
		})
	}
}

// Test flow, for each route:
// 1. Make the chain lookup for a valid granter fail and send a request for it.
// 2. Send the same request again within the error TTL.
// 3. Require both requests to be rejected as an unknown validator and the chain to be queried once.
func TestAuthzRoutes_RepeatedFailingGranterDoesNotReachChain(t *testing.T) {
	for _, route := range authzRoutes {
		t.Run(route.name, func(t *testing.T) {
			granter := sdk.AccAddress([]byte("granterA_20_bytes___")).String()
			chain := newAuthzRouteQueryServer(granter)
			chain.errs[granter] = status.Error(codes.Unavailable, "node down")
			server := newAuthzRouteServer(t, chain)

			requireHTTPError(t, route.call(t, server, granter), http.StatusUnauthorized, "validator not found")
			requireHTTPError(t, route.call(t, server, granter), http.StatusUnauthorized, "validator not found")
			require.Equal(t, 1, chain.callCount(granter))
		})
	}
}
