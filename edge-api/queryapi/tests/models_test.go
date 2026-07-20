package queryapitest

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Shared test data helpers
// ---------------------------------------------------------------------------

func makeModel(id string, unitsPerToken uint64) inferencetypes.Model {
	return inferencetypes.Model{
		Id:                     id,
		UnitsOfComputePerToken: unitsPerToken,
	}
}

// ---------------------------------------------------------------------------
// GetModels
// ---------------------------------------------------------------------------

type stubModelsServer struct {
	inferencetypes.UnimplementedQueryServer
	epochGroupData inferencetypes.EpochGroupData
	subGroupData   map[string]inferencetypes.EpochGroupData
}

func (s *stubModelsServer) CurrentEpochGroupData(_ context.Context, _ *inferencetypes.QueryCurrentEpochGroupDataRequest) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error) {
	return &inferencetypes.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: s.epochGroupData,
	}, nil
}

func (s *stubModelsServer) EpochGroupData(_ context.Context, req *inferencetypes.QueryGetEpochGroupDataRequest) (*inferencetypes.QueryGetEpochGroupDataResponse, error) {
	data, ok := s.subGroupData[req.ModelId]
	if !ok {
		return nil, status.Error(codes.NotFound, "model not found")
	}
	return &inferencetypes.QueryGetEpochGroupDataResponse{EpochGroupData: data}, nil
}

func TestGetModels_Returns200WithActiveModels(t *testing.T) {
	modelA := makeModel("model-a", 10)
	modelB := makeModel("model-b", 20)

	srv := &stubModelsServer{
		epochGroupData: inferencetypes.EpochGroupData{
			EpochIndex:     1,
			SubGroupModels: []string{"model-a", "model-b"},
		},
		subGroupData: map[string]inferencetypes.EpochGroupData{
			"model-a": {ModelSnapshot: &modelA},
			"model-b": {ModelSnapshot: &modelB},
		},
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/models")
	require.NoError(t, s.GetModels(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "model-a")
	assert.Contains(t, body, "model-b")
	assert.Contains(t, body, `"object":"list"`)
	assert.Contains(t, body, `"data"`)
}

func TestGetModels_SkipsMissingSubgroupModels(t *testing.T) {
	modelA := makeModel("model-a", 10)

	srv := &stubModelsServer{
		epochGroupData: inferencetypes.EpochGroupData{
			EpochIndex:     1,
			SubGroupModels: []string{"model-a", "model-missing"},
		},
		subGroupData: map[string]inferencetypes.EpochGroupData{
			"model-a": {ModelSnapshot: &modelA},
			// model-missing is absent — EpochGroupData returns NotFound
		},
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/models")
	require.NoError(t, s.GetModels(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "model-a")
	assert.NotContains(t, body, "model-missing")
}

func TestGetModels_ReturnsEmptyListWhenNoSubgroupModels(t *testing.T) {
	srv := &stubModelsServer{
		epochGroupData: inferencetypes.EpochGroupData{EpochIndex: 1},
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/models")
	require.NoError(t, s.GetModels(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "list", resp["object"])
	data, ok := resp["data"]
	require.True(t, ok)
	assert.Empty(t, data)
}

type errCurrentEpochServer struct{ inferencetypes.UnimplementedQueryServer }

func (s *errCurrentEpochServer) CurrentEpochGroupData(_ context.Context, _ *inferencetypes.QueryCurrentEpochGroupDataRequest) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error) {
	return nil, status.Error(codes.Unavailable, "chain unavailable")
}

func TestGetModels_ReturnsErrorOnChainFailure(t *testing.T) {
	s := handlersWithInference(t, &errCurrentEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/models")
	err := s.GetModels(ctx)
	require.Error(t, err)
	_ = rec
}

// ---------------------------------------------------------------------------
// GetGovernanceModels
// ---------------------------------------------------------------------------

type stubGovernanceModelsServer struct {
	inferencetypes.UnimplementedQueryServer
	models []inferencetypes.Model
}

func (s *stubGovernanceModelsServer) ModelsAll(_ context.Context, _ *inferencetypes.QueryModelsAllRequest) (*inferencetypes.QueryModelsAllResponse, error) {
	return &inferencetypes.QueryModelsAllResponse{Model: s.models}, nil
}

func TestGetGovernanceModels_Returns200WithAllModels(t *testing.T) {
	srv := &stubGovernanceModelsServer{
		models: []inferencetypes.Model{
			makeModel("governance-model-1", 5),
			makeModel("governance-model-2", 15),
		},
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/models")
	require.NoError(t, s.GetGovernanceModels(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "governance-model-1")
	assert.Contains(t, body, "governance-model-2")
	// Response shape uses "models" key.
	assert.Contains(t, body, `"models"`)
}

type errModelsAllServer struct{ inferencetypes.UnimplementedQueryServer }

func (s *errModelsAllServer) ModelsAll(_ context.Context, _ *inferencetypes.QueryModelsAllRequest) (*inferencetypes.QueryModelsAllResponse, error) {
	return nil, status.Error(codes.Unavailable, "chain unavailable")
}

func TestGetGovernanceModels_ReturnsErrorOnChainFailure(t *testing.T) {
	s := handlersWithInference(t, &errModelsAllServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/models")
	err := s.GetGovernanceModels(ctx)
	require.Error(t, err)
	_ = rec
}

// ---------------------------------------------------------------------------
// GetGovernanceModelsLegacy
// ---------------------------------------------------------------------------

func TestGetGovernanceModelsLegacy_Returns200WithLegacyShape(t *testing.T) {
	srv := &stubGovernanceModelsServer{
		models: []inferencetypes.Model{
			makeModel("legacy-model-1", 8),
		},
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/models/legacy")
	require.NoError(t, s.GetGovernanceModelsLegacy(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "legacy-model-1")
	// Legacy response shape uses "model" (singular) key.
	assert.Contains(t, body, `"model"`)
}

func TestGetGovernanceModelsLegacy_ReturnsErrorOnChainFailure(t *testing.T) {
	s := handlersWithInference(t, &errModelsAllServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/models/legacy")
	err := s.GetGovernanceModelsLegacy(ctx)
	require.Error(t, err)
	_ = rec
}

// ---------------------------------------------------------------------------
// GetPricing
// ---------------------------------------------------------------------------

type stubPricingServer struct {
	inferencetypes.UnimplementedQueryServer
	epochGroupData inferencetypes.EpochGroupData
	subGroupData   map[string]inferencetypes.EpochGroupData
}

func (s *stubPricingServer) CurrentEpochGroupData(_ context.Context, _ *inferencetypes.QueryCurrentEpochGroupDataRequest) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error) {
	return &inferencetypes.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: s.epochGroupData,
	}, nil
}

func (s *stubPricingServer) EpochGroupData(_ context.Context, req *inferencetypes.QueryGetEpochGroupDataRequest) (*inferencetypes.QueryGetEpochGroupDataResponse, error) {
	data, ok := s.subGroupData[req.ModelId]
	if !ok {
		return nil, status.Error(codes.NotFound, "model not found")
	}
	return &inferencetypes.QueryGetEpochGroupDataResponse{EpochGroupData: data}, nil
}

func TestGetPricing_Returns200WithPricing(t *testing.T) {
	model := makeModel("priced-model", 10)

	srv := &stubPricingServer{
		epochGroupData: inferencetypes.EpochGroupData{
			EpochIndex:         1,
			UnitOfComputePrice: 5,
			SubGroupModels:     []string{"priced-model"},
		},
		subGroupData: map[string]inferencetypes.EpochGroupData{
			"priced-model": {ModelSnapshot: &model},
		},
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/pricing")
	require.NoError(t, s.GetPricing(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, float64(5), resp["unit_of_compute_price"])

	modelsAny, ok := resp["models"]
	require.True(t, ok)
	models := modelsAny.([]any)
	require.Len(t, models, 1)

	m := models[0].(map[string]any)
	assert.Equal(t, "priced-model", m["id"])
	// price_per_token = units_of_compute_per_token * unit_of_compute_price = 10 * 5 = 50
	assert.Equal(t, float64(50), m["price_per_token"])
}

func TestGetPricing_ReturnsErrorOnChainFailure(t *testing.T) {
	s := handlersWithInference(t, &errCurrentEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/pricing")
	err := s.GetPricing(ctx)
	require.Error(t, err)
	_ = rec
}

// ---------------------------------------------------------------------------
// GetGovernancePricing
// ---------------------------------------------------------------------------

type stubGovernancePricingServer struct {
	inferencetypes.UnimplementedQueryServer
	epochGroupData  inferencetypes.EpochGroupData
	models          []inferencetypes.Model
	perTokenPrices  []inferencetypes.ModelPrice
}

func (s *stubGovernancePricingServer) CurrentEpochGroupData(_ context.Context, _ *inferencetypes.QueryCurrentEpochGroupDataRequest) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error) {
	return &inferencetypes.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: s.epochGroupData,
	}, nil
}

func (s *stubGovernancePricingServer) ModelsAll(_ context.Context, _ *inferencetypes.QueryModelsAllRequest) (*inferencetypes.QueryModelsAllResponse, error) {
	return &inferencetypes.QueryModelsAllResponse{Model: s.models}, nil
}

func (s *stubGovernancePricingServer) GetAllModelPerTokenPrices(_ context.Context, _ *inferencetypes.QueryGetAllModelPerTokenPricesRequest) (*inferencetypes.QueryGetAllModelPerTokenPricesResponse, error) {
	return &inferencetypes.QueryGetAllModelPerTokenPricesResponse{
		ModelPrices: s.perTokenPrices,
	}, nil
}

func TestGetGovernancePricing_Returns200WithLegacyPricing(t *testing.T) {
	srv := &stubGovernancePricingServer{
		epochGroupData: inferencetypes.EpochGroupData{UnitOfComputePrice: 4},
		models: []inferencetypes.Model{
			makeModel("gov-model", 10),
		},
		// No per-token prices → dynamic pricing disabled.
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/pricing")
	require.NoError(t, s.GetGovernancePricing(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, float64(4), resp["unit_of_compute_price"])
	assert.Equal(t, false, resp["dynamic_pricing_enabled"])

	modelsAny := resp["models"].([]any)
	require.Len(t, modelsAny, 1)
	m := modelsAny[0].(map[string]any)
	assert.Equal(t, "gov-model", m["id"])
	// price_per_token = 10 * 4 = 40
	assert.Equal(t, float64(40), m["price_per_token"])
}

func TestGetGovernancePricing_UsesDynamicPriceWhenAvailable(t *testing.T) {
	srv := &stubGovernancePricingServer{
		epochGroupData: inferencetypes.EpochGroupData{UnitOfComputePrice: 4},
		models: []inferencetypes.Model{
			makeModel("dyn-model", 10),
		},
		// Dynamic price overrides legacy for dyn-model.
		perTokenPrices: []inferencetypes.ModelPrice{
			{ModelId: "dyn-model", Price: 99},
		},
	}

	s := handlersWithInference(t, srv)
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/pricing")
	require.NoError(t, s.GetGovernancePricing(ctx))
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, true, resp["dynamic_pricing_enabled"])

	modelsAny := resp["models"].([]any)
	require.Len(t, modelsAny, 1)
	m := modelsAny[0].(map[string]any)
	// Dynamic price should override legacy price.
	assert.Equal(t, float64(99), m["price_per_token"])
}

func TestGetGovernancePricing_ReturnsErrorOnCurrentEpochFailure(t *testing.T) {
	s := handlersWithInference(t, &errCurrentEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/pricing")
	err := s.GetGovernancePricing(ctx)
	require.Error(t, err)
	_ = rec
}

type errModelsAllWithEpochServer struct {
	inferencetypes.UnimplementedQueryServer
}

func (s *errModelsAllWithEpochServer) CurrentEpochGroupData(_ context.Context, _ *inferencetypes.QueryCurrentEpochGroupDataRequest) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error) {
	return &inferencetypes.QueryCurrentEpochGroupDataResponse{}, nil
}

func (s *errModelsAllWithEpochServer) ModelsAll(_ context.Context, _ *inferencetypes.QueryModelsAllRequest) (*inferencetypes.QueryModelsAllResponse, error) {
	return nil, status.Error(codes.Unavailable, "chain unavailable")
}

func TestGetGovernancePricing_ReturnsErrorOnModelsAllFailure(t *testing.T) {
	s := handlersWithInference(t, &errModelsAllWithEpochServer{})
	ctx, rec := echoContext(t, http.MethodGet, "/v1/governance/pricing")
	err := s.GetGovernancePricing(ctx)
	require.Error(t, err)
	_ = rec
}
