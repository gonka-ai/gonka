package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"decentralized-api/cosmosclient"
)

func TestTopParticipantCount_SumsModels(t *testing.T) {
	commits := []*types.PoCV2StoreCommitWithAddress{
		{ParticipantAddress: "a", Count: 10, ModelId: "m1"},
		{ParticipantAddress: "a", Count: 5, ModelId: "m2"},
		{ParticipantAddress: "b", Count: 12, ModelId: "m1"},
	}
	count, models, ok := topParticipantStats(commits, 0, 1, 0)
	require.True(t, ok)
	require.Equal(t, uint64(15), count)
	require.Equal(t, uint64(2), models)
}

func TestTopParticipantStats_PrefersHigherFee(t *testing.T) {
	commits := []*types.PoCV2StoreCommitWithAddress{
		{ParticipantAddress: "a", Count: 100, ModelId: "m1"},
		{ParticipantAddress: "b", Count: 10, ModelId: "m1"},
		{ParticipantAddress: "b", Count: 10, ModelId: "m2"},
		{ParticipantAddress: "b", Count: 10, ModelId: "m3"},
		{ParticipantAddress: "b", Count: 4, ModelId: "m1"},
	}
	// a: 100×100 + 1×100_000. b: max(10,4)+10+10 = 30 counts, 3 models.
	count, models, ok := topParticipantStats(commits, 0, 100, 100_000)
	require.True(t, ok)
	require.Equal(t, uint64(30), count)
	require.Equal(t, uint64(3), models)
}

func TestMaxConfirmationPocs_Disabled(t *testing.T) {
	ep := types.DefaultEpochParams()
	require.Equal(t, uint64(0), maxConfirmationPocs(ep, &types.ConfirmationPoCParams{}))
}

func TestMaxConfirmationPocs_FitsWindow(t *testing.T) {
	ep := types.DefaultEpochParams()
	ep.EpochLength = 20_000
	ep.ConfirmationPocSafetyWindow = 50
	cp := &types.ConfirmationPoCParams{ExpectedConfirmationsPerEpoch: 1}
	n := maxConfirmationPocs(ep, cp)
	require.GreaterOrEqual(t, n, uint64(1))
}

func TestConfirmationPocEventSpan_IgnoresSafetyWindow(t *testing.T) {
	ep := types.DefaultEpochParams()
	ep.ConfirmationPocSafetyWindow = 50
	span := confirmationPocEventSpan(ep)
	ep.ConfirmationPocSafetyWindow = 5_000
	require.Equal(t, span, confirmationPocEventSpan(ep))
}

func TestMaxConfirmationPocs_SafetyWindowDoesNotInflateSpacing(t *testing.T) {
	ep := types.DefaultEpochParams()
	ep.EpochLength = 20_000
	ep.ConfirmationPocSafetyWindow = 50
	cp := &types.ConfirmationPoCParams{ExpectedConfirmationsPerEpoch: 1}
	withSafety := maxConfirmationPocs(ep, cp)

	// Old occupied = grace + phases + safety. Span excludes safety, so more
	// sequential CPoCs fit than if safety were treated as inter-event delay.
	grace := ep.InferenceValidationCutoff
	if grace < 1 {
		grace = 1
	}
	oldOccupied := grace + ep.PocStageDuration + ep.PocExchangeDuration +
		ep.PocValidationDelay + ep.PocValidationDuration +
		ep.SetNewValidatorsDelay + ep.ConfirmationPocSafetyWindow
	ec := types.EpochContext{EpochIndex: 2, PocStartBlockHeight: ep.EpochLength, EpochParams: *ep}
	confirmationWindow := oldOccupied - grace
	triggerWindowEnd := ec.NextPoCStart() - ep.InferenceValidationCutoff - confirmationWindow
	triggerWindowLength := triggerWindowEnd - ec.SetNewValidators() + 1
	oldN := triggerWindowLength / oldOccupied
	if oldN < 1 {
		oldN = 1
	}
	require.GreaterOrEqual(t, withSafety, uint64(oldN))
}

func TestEpochFeeBudgetNgonka_ZeroWhenFeesOff(t *testing.T) {
	fp := types.DefaultFeeParams()
	fp.EnabledFeeGroups = nil
	got := epochFeeBudgetNgonka(fp, types.DefaultEpochParams(), types.DefaultConfirmationPoCParams(), 10_000, 0)
	require.True(t, got.IsZero())
}

func TestEpochBudgetKnown(t *testing.T) {
	off := types.DefaultFeeParams()
	off.EnabledFeeGroups = nil
	require.True(t, epochBudgetKnown(off, countSourceNone, countSourceNone), "fees off: budget is 0")

	on := types.DefaultFeeParams()
	on.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	g := on.GroupByName(types.FeeGroupEpoch)
	require.NotNil(t, g)
	g.MinGasPrice = 1000
	require.False(t, epochBudgetKnown(on, countSourceNone, countSourceNone))
	require.True(t, epochBudgetKnown(on, countSourceParam, countSourceNone), "no static rate: count is enough")

	withStatic := types.DefaultFeeParams()
	withStatic.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	withStatic.GroupByName(types.FeeGroupEpoch).MinGasPrice = 1000
	addStoreCommitStatic(withStatic, types.StoreCommitGasPerNewKey)
	require.False(t, epochBudgetKnown(withStatic, countSourceParam, countSourceNone))
	require.True(t, epochBudgetKnown(withStatic, countSourceParam, countSourceParam))
	require.True(t, epochBudgetKnown(withStatic, countSourceTopParticipant, countSourceTopParticipant))
}

func TestEpochFeeBudgetNgonka_CoversCountAndStages(t *testing.T) {
	fp := types.DefaultFeeParams()
	fp.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	epoch := fp.GroupByName(types.FeeGroupEpoch)
	require.NotNil(t, epoch)
	epoch.MinGasPrice = 1000
	addStoreCommitStatic(fp, types.StoreCommitGasPerNewKey)

	ep := types.DefaultEpochParams()
	ep.EpochLength = 20_000
	cp := &types.ConfirmationPoCParams{ExpectedConfirmationsPerEpoch: 1}

	got := epochFeeBudgetNgonka(fp, ep, cp, 10_000, 0)
	require.True(t, got.IsPositive())
	zeroCount := epochFeeBudgetNgonka(fp, ep, cp, 0, 0)
	withKeys := epochFeeBudgetNgonka(fp, ep, cp, 10_000, 3)
	require.True(t, withKeys.GT(got))
	require.True(t, got.GT(zeroCount))
}

func TestGetEpochFeeBudget_HTTPCountParam(t *testing.T) {
	s, _, _ := setupTestServer(t)

	fp := types.DefaultFeeParams()
	fp.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	fp.GroupByName(types.FeeGroupEpoch).MinGasPrice = 1000
	addStoreCommitStatic(fp, types.StoreCommitGasPerNewKey)
	ep := types.DefaultEpochParams()
	ep.EpochLength = 20_000
	params := types.DefaultParams()
	params.FeeParams = fp
	params.EpochParams = ep
	params.ConfirmationPocParams = &types.ConfirmationPoCParams{ExpectedConfirmationsPerEpoch: 1}

	qc := s.recorder.NewInferenceQueryClient().(*mockInferenceQueryClient)
	qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{
		Params:      params,
		LatestEpoch: types.Epoch{Index: 10, PocStartBlockHeight: 1000},
	}, nil)

	rec := s.recorder.(*cosmosclient.MockCosmosMessageClient)
	rec.On("GetAccountAddress").Return("gonka1payer")
	rec.On("GetSignerAddress").Return("gonka1payer")
	rec.On("GetClientContext").Return(sdkclient.Context{})
	rec.On("BankBalances", mock.Anything, "gonka1payer").Return(
		[]sdk.Coin{sdk.NewInt64Coin(types.BaseCoin, 9_000_000_000_000_000)}, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/epoch-fee-budget?count=100", nil)
	recw := httptest.NewRecorder()
	s.e.ServeHTTP(recw, req)
	require.Equal(t, http.StatusOK, recw.Code, recw.Body.String())

	var body epochFeeBudgetResponse
	require.NoError(t, json.Unmarshal(recw.Body.Bytes(), &body))
	assert.Equal(t, types.BaseCoin, body.Denom)
	assert.Equal(t, uint64(100), body.Count)
	assert.Equal(t, countSourceParam, body.CountSource)
	assert.Equal(t, uint64(0), body.ServingModels)
	assert.Equal(t, countSourceNone, body.ServingModelsSource)
	assert.False(t, body.BudgetKnown)
	assert.Equal(t, "9000000000000000", body.SpendableBalance)
	assert.False(t, body.SpendableCoversBudget)
}

func TestGetEpochFeeBudget_CountAndServingModels(t *testing.T) {
	s, _, _ := setupTestServer(t)

	fp := types.DefaultFeeParams()
	fp.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	fp.GroupByName(types.FeeGroupEpoch).MinGasPrice = 1000
	addStoreCommitStatic(fp, types.StoreCommitGasPerNewKey)
	ep := types.DefaultEpochParams()
	ep.EpochLength = 20_000
	params := types.DefaultParams()
	params.FeeParams = fp
	params.EpochParams = ep
	params.ConfirmationPocParams = &types.ConfirmationPoCParams{ExpectedConfirmationsPerEpoch: 1}

	qc := s.recorder.NewInferenceQueryClient().(*mockInferenceQueryClient)
	qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{
		Params:      params,
		LatestEpoch: types.Epoch{Index: 10, PocStartBlockHeight: 1000},
	}, nil)

	rec := s.recorder.(*cosmosclient.MockCosmosMessageClient)
	rec.On("GetAccountAddress").Return("gonka1payer")
	rec.On("GetSignerAddress").Return("gonka1payer")
	rec.On("GetClientContext").Return(sdkclient.Context{})
	rec.On("BankBalances", mock.Anything, "gonka1payer").Return(
		[]sdk.Coin{sdk.NewInt64Coin(types.BaseCoin, 9_000_000_000_000_000)}, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/epoch-fee-budget?count=100&serving_models=3", nil)
	recw := httptest.NewRecorder()
	s.e.ServeHTTP(recw, req)
	require.Equal(t, http.StatusOK, recw.Code, recw.Body.String())

	expected := epochFeeBudgetNgonka(fp, ep, params.ConfirmationPocParams, 100, 3)
	var body epochFeeBudgetResponse
	require.NoError(t, json.Unmarshal(recw.Body.Bytes(), &body))
	assert.Equal(t, uint64(100), body.Count)
	assert.Equal(t, uint64(3), body.ServingModels)
	assert.Equal(t, countSourceParam, body.CountSource)
	assert.Equal(t, countSourceParam, body.ServingModelsSource)
	assert.True(t, body.BudgetKnown)
	assert.Equal(t, expected.String(), body.BudgetBalance)
	assert.True(t, body.SpendableCoversBudget)
}

func addStoreCommitStatic(fp *types.FeeParams, gas uint64) {
	fp.GroupByName(types.FeeGroupEpoch).Msgs = append(fp.GroupByName(types.FeeGroupEpoch).Msgs, &types.MsgGasRule{
		TypeUrl: sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}),
		Func: &types.MsgGasRule_Static{
			Static: &types.StaticParams{Gas: gas},
		},
	})
}

func TestGetEpochFeeBudget_UnknownCountDoesNotCover(t *testing.T) {
	s, _, _ := setupTestServer(t)

	fp := types.DefaultFeeParams()
	fp.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	fp.GroupByName(types.FeeGroupEpoch).MinGasPrice = 1000
	ep := types.DefaultEpochParams()
	params := types.DefaultParams()
	params.FeeParams = fp
	params.EpochParams = ep
	params.ConfirmationPocParams = &types.ConfirmationPoCParams{ExpectedConfirmationsPerEpoch: 1}

	qc := s.recorder.NewInferenceQueryClient().(*mockInferenceQueryClient)
	qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{
		Params:      params,
		LatestEpoch: types.Epoch{Index: 10, PocStartBlockHeight: 1000},
	}, nil)
	qc.On("AllPoCV2StoreCommitsForStage", mock.Anything, mock.Anything).Return(
		&types.QueryAllPoCV2StoreCommitsForStageResponse{}, nil)

	rec := s.recorder.(*cosmosclient.MockCosmosMessageClient)
	rec.On("GetAccountAddress").Return("gonka1payer")
	rec.On("GetSignerAddress").Return("gonka1payer")
	rec.On("GetClientContext").Return(sdkclient.Context{})
	rec.On("BankBalances", mock.Anything, "gonka1payer").Return(
		[]sdk.Coin{sdk.NewInt64Coin(types.BaseCoin, 1_000_000_000_000)}, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/epoch-fee-budget", nil)
	recw := httptest.NewRecorder()
	s.e.ServeHTTP(recw, req)
	require.Equal(t, http.StatusOK, recw.Code, recw.Body.String())

	var body epochFeeBudgetResponse
	require.NoError(t, json.Unmarshal(recw.Body.Bytes(), &body))
	assert.Equal(t, uint64(0), body.Count)
	assert.Equal(t, countSourceNone, body.CountSource)
	assert.False(t, body.BudgetKnown)
	assert.False(t, body.SpendableCoversBudget)
}

func TestGetEpochFeeBudget_FeesDisabledKnownZero(t *testing.T) {
	s, _, _ := setupTestServer(t)

	params := types.DefaultParams()
	params.FeeParams.EnabledFeeGroups = nil
	qc := s.recorder.NewInferenceQueryClient().(*mockInferenceQueryClient)
	qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{
		Params:      params,
		LatestEpoch: types.Epoch{Index: 10, PocStartBlockHeight: 1000},
	}, nil)
	qc.On("AllPoCV2StoreCommitsForStage", mock.Anything, mock.Anything).Return(
		&types.QueryAllPoCV2StoreCommitsForStageResponse{}, nil)

	rec := s.recorder.(*cosmosclient.MockCosmosMessageClient)
	rec.On("GetAccountAddress").Return("gonka1payer")
	rec.On("GetSignerAddress").Return("gonka1payer")
	rec.On("GetClientContext").Return(sdkclient.Context{})
	rec.On("BankBalances", mock.Anything, "gonka1payer").Return(
		[]sdk.Coin{sdk.NewInt64Coin(types.BaseCoin, 0)}, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/epoch-fee-budget", nil)
	recw := httptest.NewRecorder()
	s.e.ServeHTTP(recw, req)
	require.Equal(t, http.StatusOK, recw.Code, recw.Body.String())

	var body epochFeeBudgetResponse
	require.NoError(t, json.Unmarshal(recw.Body.Bytes(), &body))
	assert.Equal(t, "0", body.BudgetBalance)
	assert.True(t, body.BudgetKnown)
	assert.True(t, body.SpendableCoversBudget)
}

func TestGetEpochFeeBudget_BadCount(t *testing.T) {
	s, _, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/epoch-fee-budget?count=nope", nil)
	recw := httptest.NewRecorder()
	s.e.ServeHTTP(recw, req)
	require.Equal(t, http.StatusBadRequest, recw.Code)
}

func TestGetEpochFeeBudget_BadServingModels(t *testing.T) {
	s, _, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/epoch-fee-budget?serving_models=nope", nil)
	recw := httptest.NewRecorder()
	s.e.ServeHTTP(recw, req)
	require.Equal(t, http.StatusBadRequest, recw.Code)
}
