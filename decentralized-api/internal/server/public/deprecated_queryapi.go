package public

import (
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/labstack/echo/v4"
	blstypes "github.com/productscience/inference/x/bls/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"

	"common/chain"
	"common/queryapi"
	"common/queryapi/gen"
	"decentralized-api/cosmosclient"
)

const (
	deprecatedQueryAPISuccessor = `</v1/>; rel="successor-version"; title="edge-api"`
)

// recorderChainClient adapts CosmosMessageClient to queryapi.ChainClient so
// deprecated dapi dual-serve routes use the same common/chain-backed handlers
// as edge-api.
type recorderChainClient struct {
	recorder cosmosclient.CosmosMessageClient
}

func (c recorderChainClient) InferenceQueryClient() chain.InferenceClient {
	return c.recorder.NewInferenceQueryClient()
}

func (c recorderChainClient) BLSQueryClient() blstypes.QueryClient {
	return c.recorder.NewBLSQueryClient()
}

func (c recorderChainClient) RestrictionsQueryClient() restrictionstypes.QueryClient {
	return c.recorder.NewRestrictionsQueryClient()
}

func (c recorderChainClient) CometServiceClient() cmtservice.ServiceClient {
	return c.recorder.NewCometQueryClient()
}

// deprecatedQueryAPI wraps queryapi handlers and marks every response deprecated.
// Old proxies that still forward Tier A /v1 reads to dapi keep working; new
// proxies should route these to edge-api.
type deprecatedQueryAPI struct {
	inner gen.ServerInterface
}

func markQueryAPIDeprecated(c echo.Context) {
	c.Response().Header().Set("Deprecation", "true")
	c.Response().Header().Set("Link", deprecatedQueryAPISuccessor)
}

func (d deprecatedQueryAPI) GetBLSEpoch(ctx echo.Context, id gen.Uint64) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetBLSEpoch(ctx, id)
}

func (d deprecatedQueryAPI) GetBLSEpochs(ctx echo.Context, id gen.Uint64) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetBLSEpochs(ctx, id)
}

func (d deprecatedQueryAPI) GetBLSSignature(ctx echo.Context, requestId string) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetBLSSignature(ctx, requestId)
}

func (d deprecatedQueryAPI) GetBridgeAddresses(ctx echo.Context, params gen.GetBridgeAddressesParams) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetBridgeAddresses(ctx, params)
}

func (d deprecatedQueryAPI) DebugPubKeyToAddr(ctx echo.Context, pubkey string) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.DebugPubKeyToAddr(ctx, pubkey)
}

func (d deprecatedQueryAPI) DebugVerifyBlockSignatures(ctx echo.Context, height gen.Int64) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.DebugVerifyBlockSignatures(ctx, height)
}

func (d deprecatedQueryAPI) GetEpoch(ctx echo.Context, epoch string) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetEpoch(ctx, epoch)
}

func (d deprecatedQueryAPI) GetEpochParticipants(ctx echo.Context, epoch string) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetEpochParticipants(ctx, epoch)
}

func (d deprecatedQueryAPI) GetGovernanceModels(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetGovernanceModels(ctx)
}

func (d deprecatedQueryAPI) GetGovernanceModelsLegacy(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetGovernanceModelsLegacy(ctx)
}

func (d deprecatedQueryAPI) GetModels(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetModels(ctx)
}

func (d deprecatedQueryAPI) GetParticipants(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetParticipants(ctx)
}

func (d deprecatedQueryAPI) GetParticipant(ctx echo.Context, address string) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetParticipant(ctx, address)
}

func (d deprecatedQueryAPI) GetPoCBatches(ctx echo.Context, epoch gen.Int64) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetPoCBatches(ctx, epoch)
}

func (d deprecatedQueryAPI) GetPricing(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetPricing(ctx)
}

func (d deprecatedQueryAPI) GetRestrictionsExemptions(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetRestrictionsExemptions(ctx)
}

func (d deprecatedQueryAPI) GetRestrictionsExemptionUsage(ctx echo.Context, id string, account string) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetRestrictionsExemptionUsage(ctx, id, account)
}

func (d deprecatedQueryAPI) GetRestrictionsStatus(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetRestrictionsStatus(ctx)
}

func (d deprecatedQueryAPI) GetStatus(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.GetStatus(ctx)
}

func (d deprecatedQueryAPI) PostVerifyBlock(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.PostVerifyBlock(ctx)
}

func (d deprecatedQueryAPI) PostVerifyProof(ctx echo.Context) error {
	markQueryAPIDeprecated(ctx)
	return d.inner.PostVerifyProof(ctx)
}

func (d deprecatedQueryAPI) GetVersions(ctx echo.Context) error {
	// Satisfies ServerInterface for RegisterHandlers only. /v1/versions is not
	// dual-served: NewServer remounts the broker-enriched first-class handler.
	return echo.ErrNotFound
}

var _ gen.ServerInterface = deprecatedQueryAPI{}

// mountDeprecatedQueryAPIRoutes re-publishes Tier A (and optional verify/debug)
// read routes on dapi for old proxies that still forward /v1/* here. Responses
// are served by common/queryapi and marked Deprecation: true.
//
// GET /v1/versions is registered by RegisterHandlers but immediately replaced
// by NewServer with the broker-enriched first-class handler (not deprecated).
func (s *Server) mountDeprecatedQueryAPIRoutes(e *echo.Echo) {
	handlers := deprecatedQueryAPI{inner: queryapi.NewHandlers(recorderChainClient{recorder: s.recorder})}
	gen.RegisterHandlers(e, handlers)

	// Not part of edge-api Tier A, but still required by legacy bridge/clients
	// that call dapi directly.
	e.GET("/v1/bridge/block/latest", deprecatedHandler(s.getLatestBridgeBlock))
	e.GET("/v1/supply/total", deprecatedHandler(s.getTotalSupply))
}

func deprecatedHandler(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		markQueryAPIDeprecated(c)
		return next(c)
	}
}
