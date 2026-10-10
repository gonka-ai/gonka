package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cosmossdk.io/math"
	"cosmossdk.io/x/feegrant"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"decentralized-api/cosmosclient"
)

type setupBankQuery struct {
	banktypes.UnimplementedQueryServer
	spendable sdk.Coins
	err       error
}

func (q *setupBankQuery) SpendableBalances(_ context.Context, req *banktypes.QuerySpendableBalancesRequest) (*banktypes.QuerySpendableBalancesResponse, error) {
	if req.Address != "cold" {
		return nil, status.Error(codes.InvalidArgument, "must query the cold payer")
	}
	return &banktypes.QuerySpendableBalancesResponse{Balances: q.spendable}, q.err
}

type setupAllowanceQuery struct {
	feegrant.UnimplementedQueryServer
	grant *feegrant.Grant
	err   error
}

func (q *setupAllowanceQuery) Allowance(_ context.Context, req *feegrant.QueryAllowanceRequest) (*feegrant.QueryAllowanceResponse, error) {
	if req.Granter != "cold" || req.Grantee != "warm" {
		return nil, status.Error(codes.InvalidArgument, "must query cold-to-warm grant")
	}
	return &feegrant.QueryAllowanceResponse{Allowance: q.grant}, q.err
}

func setupFeegrantServer(t *testing.T, bank *setupBankQuery, grant *setupAllowanceQuery, coldSigner bool) (*Server, *mockInferenceQueryClient) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	banktypes.RegisterQueryServer(server, bank)
	feegrant.RegisterQueryServer(server, grant)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///setup-report", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	recorder := &cosmosclient.MockCosmosMessageClient{}
	qc := &mockInferenceQueryClient{}
	s := &Server{recorder: recorder, cdc: getCodec(), e: echo.New()}
	s.e.GET("/admin/v1/setup/report", s.getSetupReport)
	s.e.GET("/admin/v1/epoch-fee-budget", s.getEpochFeeBudget)
	recorder.On("GetAccountAddress").Return("cold")
	signer := "warm"
	if coldSigner {
		signer = "cold"
	}
	recorder.On("GetSignerAddress").Return(signer)
	recorder.On("GetClientContext").Return(sdkclient.Context{}.WithCodec(s.cdc).WithGRPCClient(conn))
	recorder.On("NewInferenceQueryClient").Return(qc)
	// No BankBalances expectation: total balance may include vesting funds and
	// must never rescue an unsuccessful spendable-balance query in this check.
	return s, qc
}

func setupTestGrant(t *testing.T, allowance feegrant.FeeAllowanceI) *feegrant.Grant {
	t.Helper()
	value, err := codectypes.NewAnyWithValue(allowance.(proto.Message))
	require.NoError(t, err)
	return &feegrant.Grant{Granter: "cold", Grantee: "warm", Allowance: value}
}

func setupFeeParams() types.Params {
	params := types.DefaultParams()
	params.FeeParams.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	params.FeeParams.GroupByName(types.FeeGroupEpoch).MinGasPrice = 1
	params.ConfirmationPocParams = &types.ConfirmationPoCParams{}
	return params
}

func TestCheckFeegrant_EpochAffordability(t *testing.T) {
	params := setupFeeParams()
	budget := epochFeeBudgetNgonka(params.FeeParams, params.EpochParams, params.ConfirmationPocParams, 10)
	coins := func(n math.Int) sdk.Coins { return sdk.NewCoins(sdk.NewCoin(types.BaseCoin, n)) }
	soon := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name       string
		spendable  math.Int
		allowance  feegrant.FeeAllowanceI
		coldSigner bool
		unknown    bool
		feesOff    bool
		want       CheckStatus
	}{
		{"zero cold balance with unlimited grant", math.ZeroInt(), &feegrant.BasicAllowance{}, false, false, false, FAIL},
		{"one below budget", budget.SubRaw(1), &feegrant.BasicAllowance{}, false, false, false, FAIL},
		{"exact budget", budget, &feegrant.BasicAllowance{}, false, false, false, PASS},
		{"above budget", budget.AddRaw(1), &feegrant.BasicAllowance{}, false, false, false, PASS},
		{"allowance caps usable funds", budget.MulRaw(2), &feegrant.BasicAllowance{SpendLimit: coins(budget.SubRaw(1))}, false, false, false, FAIL},
		{"allowance equals budget", budget.MulRaw(2), &feegrant.BasicAllowance{SpendLimit: coins(budget)}, false, false, false, PASS},
		{"expiring grant cannot bypass balance check", math.ZeroInt(), &feegrant.BasicAllowance{Expiration: &soon}, false, false, false, FAIL},
		{"cold signer without grant empty", math.ZeroInt(), nil, true, false, false, FAIL},
		{"cold signer without grant funded", budget, nil, true, false, false, PASS},
		{"unknown count positive funds", math.OneInt(), &feegrant.BasicAllowance{}, false, true, false, PASS},
		{"unknown count zero funds", math.ZeroInt(), &feegrant.BasicAllowance{}, false, true, false, FAIL},
		{"fees disabled", math.ZeroInt(), &feegrant.BasicAllowance{}, false, false, true, PASS},
		{"cold signer fees disabled", math.ZeroInt(), nil, true, false, true, PASS},
		{"periodic remaining caps balance", budget.MulRaw(2), &feegrant.PeriodicAllowance{Period: time.Hour, PeriodSpendLimit: coins(budget), PeriodCanSpend: coins(budget.SubRaw(1)), PeriodReset: soon}, false, false, false, FAIL},
		{"periodic allowance resets", budget, &feegrant.PeriodicAllowance{Period: time.Hour, PeriodSpendLimit: coins(budget), PeriodCanSpend: coins(math.ZeroInt()), PeriodReset: time.Now().Add(-time.Hour)}, false, false, false, PASS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := &setupAllowanceQuery{}
			if tc.allowance != nil {
				grant.grant = setupTestGrant(t, tc.allowance)
			}
			s, qc := setupFeegrantServer(t, &setupBankQuery{spendable: coins(tc.spendable)}, grant, tc.coldSigner)
			p := setupFeeParams()
			if tc.feesOff {
				p.FeeParams.EnabledFeeGroups = nil
			}
			qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{Params: p, LatestEpoch: types.Epoch{PocStartBlockHeight: 123}}, nil)
			commits := []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "a", Count: 6}, {ParticipantAddress: "a", Count: 4}, {ParticipantAddress: "b", Count: 8}}
			if tc.unknown {
				commits = nil
			}
			qc.On("AllPoCV2StoreCommitsForStage", mock.Anything, &types.QueryAllPoCV2StoreCommitsForStageRequest{PocStageStartBlockHeight: 123}).Return(&types.QueryAllPoCV2StoreCommitsForStageResponse{Commits: commits}, nil)

			check := s.checkFeegrant(context.Background())
			require.Equal(t, "feegrant_allowance", check.ID)
			require.Equal(t, tc.want, check.Status, check.Message)
			if !tc.feesOff {
				details := check.Details.(map[string]interface{})
				require.Equal(t, !tc.unknown, details["budget_known"])
				if tc.unknown {
					require.Equal(t, countSourceNone, details["count_source"])
				} else {
					require.Equal(t, uint64(10), details["count"])
					require.Equal(t, budget.String(), details["budget_balance"])
				}
			}
			if tc.name == "exact budget" {
				// The independently served budget endpoint must agree with the check.
				rw := httptest.NewRecorder()
				s.e.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/admin/v1/epoch-fee-budget", nil))
				require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
				var endpoint epochFeeBudgetResponse
				require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &endpoint))
				require.Equal(t, budget.String(), endpoint.BudgetBalance)
				require.True(t, endpoint.SpendableCoversBudget)
			}
		})
	}
}

func TestCheckFeegrant_UnavailableAndInvalidGrant(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	expiredInner := setupTestGrant(t, &feegrant.BasicAllowance{Expiration: &expired}).Allowance
	for _, tc := range []struct {
		name     string
		grant    *feegrant.Grant
		grantErr error
		bankErr  error
		epochErr error
		countErr error
		feesOff  bool
		want     CheckStatus
	}{
		{name: "missing grant", want: FAIL},
		{name: "not found grant", grantErr: status.Error(codes.NotFound, "not found"), want: FAIL},
		{name: "expired grant", grant: setupTestGrant(t, &feegrant.BasicAllowance{Expiration: &expired}), want: FAIL},
		{name: "expired grant with fees off", grant: setupTestGrant(t, &feegrant.BasicAllowance{Expiration: &expired}), feesOff: true, want: FAIL},
		{name: "expired wrapped grant with fees off", grant: setupTestGrant(t, &feegrant.AllowedMsgAllowance{Allowance: expiredInner, AllowedMessages: []string{sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{})}}), feesOff: true, want: FAIL},
		{name: "missing grant with fees off", feesOff: true, want: FAIL},
		{name: "grant query failed", grantErr: status.Error(codes.Unavailable, "offline"), want: UNAVAILABLE},
		{name: "invalid grant encoding", grant: &feegrant.Grant{Allowance: &codectypes.Any{TypeUrl: "/invalid", Value: []byte("bad")}}, want: UNAVAILABLE},
		{name: "missing allowance value", grant: &feegrant.Grant{}, want: UNAVAILABLE},
		{name: "empty allowance value", grant: &feegrant.Grant{Allowance: &codectypes.Any{}}, want: UNAVAILABLE},
		{name: "spendable query failed", grant: setupTestGrant(t, &feegrant.BasicAllowance{}), bankErr: status.Error(codes.Unavailable, "bank offline"), want: UNAVAILABLE},
		{name: "epoch query failed", grant: setupTestGrant(t, &feegrant.BasicAllowance{}), epochErr: errors.New("epoch offline"), want: UNAVAILABLE},
		{name: "count query failed", grant: setupTestGrant(t, &feegrant.BasicAllowance{}), countErr: errors.New("commits offline"), want: UNAVAILABLE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, qc := setupFeegrantServer(t, &setupBankQuery{spendable: sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1)), err: tc.bankErr}, &setupAllowanceQuery{grant: tc.grant, err: tc.grantErr}, false)
			p := setupFeeParams()
			if tc.feesOff {
				p.FeeParams.EnabledFeeGroups = nil
			}
			qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{Params: p, LatestEpoch: types.Epoch{PocStartBlockHeight: 123}}, tc.epochErr)
			qc.On("AllPoCV2StoreCommitsForStage", mock.Anything, mock.Anything).Return(&types.QueryAllPoCV2StoreCommitsForStageResponse{}, tc.countErr)
			check := s.checkFeegrant(context.Background())
			require.Equal(t, tc.want, check.Status, check.Message)
		})
	}
}

func TestCheckFeegrant_BudgetKnowledge(t *testing.T) {
	for _, tc := range []struct {
		name      string
		zeroPrice bool
		zeroRate  bool
		want      CheckStatus
	}{
		{"no epoch observation with funds", false, false, PASS},
		{"count independent budget with insufficient funds", false, true, FAIL},
		{"enabled group with zero epoch price", true, false, PASS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			balance := int64(1)
			if tc.zeroPrice {
				balance = 0
			}
			s, qc := setupFeegrantServer(t, &setupBankQuery{spendable: sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, balance))}, &setupAllowanceQuery{}, true)
			p := setupFeeParams()
			if tc.zeroPrice {
				p.FeeParams.GroupByName(types.FeeGroupEpoch).MinGasPrice = 0
				p.FeeParams.MinGasPriceNgonka = 1000 // Global price does not enable epoch fees.
			}
			if tc.zeroRate {
				_, rule := p.FeeParams.RuleForTypeURL(sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))
				require.NotNil(t, rule.GetStoredDelta())
				rule.GetStoredDelta().GasPerUnit = 0
			}
			// No stage means no StoreCommits query and no known count.
			qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{Params: p}, nil)
			check := s.checkFeegrant(context.Background())
			require.Equal(t, tc.want, check.Status, check.Message)
			require.Equal(t, !tc.zeroPrice, check.Details.(map[string]interface{})["fees_enabled"])
			if !tc.zeroPrice {
				details := check.Details.(map[string]interface{})
				require.Equal(t, countSourceNone, details["count_source"])
				require.Equal(t, tc.zeroRate, details["budget_known"])
			}
		})
	}
}

func TestCheckFeegrant_MissingEpochDataIsUnavailable(t *testing.T) {
	for _, info := range []*types.QueryEpochInfoResponse{nil, {}} {
		s, qc := setupFeegrantServer(t, &setupBankQuery{}, &setupAllowanceQuery{}, true)
		qc.On("EpochInfo", mock.Anything, mock.Anything).Return(info, nil)
		require.Equal(t, UNAVAILABLE, s.checkFeegrant(context.Background()).Status)
	}
}

type setupSpendableRPC struct {
	rpcclient.Client
	cdc    *codec.ProtoCodec
	called bool
}

func (q *setupSpendableRPC) ABCIQueryWithOptions(_ context.Context, path string, data cmtbytes.HexBytes, _ rpcclient.ABCIQueryOptions) (*coretypes.ResultABCIQuery, error) {
	if path != "/cosmos.bank.v1beta1.Query/SpendableBalances" {
		return nil, errors.New("unexpected bank query: " + path)
	}
	var req banktypes.QuerySpendableBalancesRequest
	if err := q.cdc.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	if req.Address != "cold" {
		return nil, errors.New("queried an account other than the cold payer")
	}
	q.called = true
	value, err := q.cdc.Marshal(&banktypes.QuerySpendableBalancesResponse{})
	return &coretypes.ResultABCIQuery{Response: abcitypes.ResponseQuery{Value: value}}, err
}

func TestCheckFeegrant_RPCOnlyUsesSpendableBalance(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	qc := &mockInferenceQueryClient{}
	s := &Server{recorder: recorder, cdc: getCodec()}
	rpc := &setupSpendableRPC{cdc: s.cdc}
	recorder.On("GetAccountAddress").Return("cold")
	recorder.On("GetSignerAddress").Return("cold")
	recorder.On("GetClientContext").Return(sdkclient.Context{}.WithCodec(s.cdc).WithClient(rpc))
	recorder.On("NewInferenceQueryClient").Return(qc)
	qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{Params: setupFeeParams()}, nil)
	check := s.checkFeegrant(context.Background())
	require.Equal(t, FAIL, check.Status, check.Message)
	require.True(t, rpc.called)
}

func TestSetupReport_CachedFeeFailureKeepsHTTP200(t *testing.T) {
	s, qc := setupFeegrantServer(t, &setupBankQuery{}, &setupAllowanceQuery{grant: setupTestGrant(t, &feegrant.BasicAllowance{})}, false)
	qc.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{Params: setupFeeParams()}, nil)
	check := s.checkFeegrant(context.Background())
	require.Equal(t, FAIL, check.Status)
	report := &SetupReport{Checks: []Check{check}, CachedUntil: time.Now().Add(time.Minute)}
	s.generateSummary(report)
	require.Equal(t, FAIL, report.OverallStatus)
	require.Contains(t, report.Summary.Recommendations[0], "cold account")
	cachedReportMutex.Lock()
	previous := cachedReport
	cachedReport = report
	cachedReportMutex.Unlock()
	t.Cleanup(func() { cachedReportMutex.Lock(); cachedReport = previous; cachedReportMutex.Unlock() })
	rw := httptest.NewRecorder()
	s.e.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/admin/v1/setup/report", nil))
	require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
	var body SetupReport
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
	require.Equal(t, FAIL, body.OverallStatus)
	require.Equal(t, FAIL, body.Checks[0].Status)
}
