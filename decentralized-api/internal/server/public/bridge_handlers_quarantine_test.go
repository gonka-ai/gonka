package public

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

const (
	quarantineTestChain  = "ethereum"
	quarantineTestPubKey = "034daac7b5be60532b562b030824b2c9b7c450985b894dd5544f9c7dd807d8bb88"
)

func quarantineTestValidator(t *testing.T) string {
	t.Helper()
	addr, err := sdk.Bech32ifyAddressBytes(sdk.GetConfig().GetBech32AccountAddrPrefix(), bytes.Repeat([]byte{1}, 20))
	require.NoError(t, err)
	return addr
}

func openQuarantineTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := apiconfig.OpenSQLite(apiconfig.SqliteConfig{Path: filepath.Join(t.TempDir(), "bridge.db")})
	require.NoError(t, err)
	require.NoError(t, apiconfig.EnsureSchema(context.Background(), db))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func quarantineTestReceipt(amount, receiptIndex string) BridgeReceipt {
	return BridgeReceipt{
		ContractAddress: "0x00000000000000000000000000000000000000aa",
		OwnerAddress:    "ignored",
		OwnerPubKey:     quarantineTestPubKey,
		Amount:          amount,
		ReceiptIndex:    receiptIndex,
	}
}

func quarantineTestBlock(number string, receipts ...BridgeReceipt) BridgeBlock {
	return BridgeBlock{
		BlockNumber:  number,
		OriginChain:  quarantineTestChain,
		ReceiptsRoot: "0xroot" + number,
		Receipts:     receipts,
	}
}

// oversizedTransferAmount mirrors bridge-geth: new(big.Int).SetBytes(log.Data)
// on 33 bytes of 0xff gives 2^264-1, an 80-digit amount.
func oversizedTransferAmount() string {
	return new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 33)).String()
}

func listQuarantine(t *testing.T, db *sql.DB) []apiconfig.BridgeQuarantinedReceipt {
	t.Helper()
	rows, err := apiconfig.ListBridgeQuarantinedReceipts(context.Background(), db, quarantineTestChain, 100)
	require.NoError(t, err)
	return rows
}

func TestCommitReceipt_OversizedAmountQuarantinedNotBroadcast(t *testing.T) {
	db := openQuarantineTestDB(t)
	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return(quarantineTestValidator(t))

	amount := oversizedTransferAmount()
	require.Len(t, amount, 80)

	q := NewBlockQueue(mockClient, db)
	err := q.commitReceipt(context.Background(), quarantineTestReceipt(amount, "0"), quarantineTestBlock("100"))
	require.NoError(t, err, "malformed receipt must advance the drain")
	mockClient.AssertNotCalled(t, "BridgeExchange", mock.Anything)

	rows := listQuarantine(t, db)
	require.Len(t, rows, 1)
	require.Equal(t, "100", rows[0].BlockNumber)
	require.Equal(t, "0", rows[0].ReceiptIndex)
	require.Equal(t, bridgeQuarantineMalformedContent, rows[0].Reason)
	require.Equal(t, 80, rows[0].AmountLen)
	require.Equal(t, amount, rows[0].AmountPrefix)
	require.Contains(t, rows[0].Error, "amount exceeds 78 digits")
}

func TestCommitReceipt_MalformedContentQuarantined(t *testing.T) {
	cases := []struct {
		name    string
		receipt BridgeReceipt
		block   BridgeBlock
		errText string
	}{
		{"zero amount", quarantineTestReceipt("0", "0"), quarantineTestBlock("100"), "amount must be > 0"},
		{"non-digit amount", quarantineTestReceipt("abc", "0"), quarantineTestBlock("100"), "amount must be a base-10"},
		{"decimal amount", quarantineTestReceipt("1.5", "0"), quarantineTestBlock("100"), "amount must be a base-10"},
		{"empty amount", quarantineTestReceipt("", "0"), quarantineTestBlock("100"), "amount is required"},
		{"79-digit amount", quarantineTestReceipt("1"+strings.Repeat("0", 78), "0"), quarantineTestBlock("100"), "amount exceeds 78 digits"},
		{"huge amount", quarantineTestReceipt(strings.Repeat("9", 1<<20), "0"), quarantineTestBlock("100"), "amount exceeds 78 digits"},
		{"non-digit receipt index", quarantineTestReceipt("1000", "x"), quarantineTestBlock("100"), "receiptIndex must be a base-10"},
		{"empty receipts root", quarantineTestReceipt("1000", "0"), BridgeBlock{BlockNumber: "100", OriginChain: quarantineTestChain}, "receiptsRoot is required"},
		{"empty contract", BridgeReceipt{OwnerPubKey: quarantineTestPubKey, Amount: "1000", ReceiptIndex: "0"}, quarantineTestBlock("100"), "contractAddress is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openQuarantineTestDB(t)
			mockClient := &cosmosclient.MockCosmosMessageClient{}
			mockClient.On("GetAccountAddress").Return(quarantineTestValidator(t))

			q := NewBlockQueue(mockClient, db)
			require.NoError(t, q.commitReceipt(context.Background(), tc.receipt, tc.block))
			mockClient.AssertNotCalled(t, "BridgeExchange", mock.Anything)

			rows := listQuarantine(t, db)
			require.Len(t, rows, 1)
			require.Equal(t, bridgeQuarantineMalformedContent, rows[0].Reason)
			require.Contains(t, rows[0].Error, tc.errText)
			require.Equal(t, len(tc.receipt.Amount), rows[0].AmountLen)
			require.LessOrEqual(t, len(rows[0].AmountPrefix), 128)
		})
	}
}

func TestCommitReceipt_MaxUint256AmountIsBroadcast(t *testing.T) {
	db := openQuarantineTestDB(t)
	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return(quarantineTestValidator(t))
	// A permanent skip keeps the test off the confirm-poll path.
	mockClient.On("BridgeExchange", mock.AnythingOfType("*types.MsgBridgeExchange")).Return(
		fmt.Errorf("bridge exchange failed: code=1 rawLog=%s", types.ErrBridgeAlreadyValidated.Error()),
	)

	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()
	require.Len(t, maxUint256, 78)

	q := NewBlockQueue(mockClient, db)
	require.NoError(t, q.commitReceipt(context.Background(), quarantineTestReceipt(maxUint256, "0"), quarantineTestBlock("100")))
	mockClient.AssertCalled(t, "BridgeExchange", mock.AnythingOfType("*types.MsgBridgeExchange"))
	require.Empty(t, listQuarantine(t, db))
}

func TestCommitReceipt_InvalidLocalValidatorPausesDrain(t *testing.T) {
	db := openQuarantineTestDB(t)
	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return("garbage")

	q := NewBlockQueue(mockClient, db)
	err := q.commitReceipt(context.Background(), quarantineTestReceipt("1000", "0"), quarantineTestBlock("100"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid local validator address")
	mockClient.AssertNotCalled(t, "BridgeExchange", mock.Anything)
	require.Empty(t, listQuarantine(t, db), "local misconfiguration must not quarantine receipts")
}

func TestCommitReceipt_MalformedPubKeyQuarantined(t *testing.T) {
	db := openQuarantineTestDB(t)
	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return(quarantineTestValidator(t))

	receipt := quarantineTestReceipt("1000", "0")
	receipt.OwnerPubKey = "zz"

	q := NewBlockQueue(mockClient, db)
	require.NoError(t, q.commitReceipt(context.Background(), receipt, quarantineTestBlock("100")))
	mockClient.AssertNotCalled(t, "BridgeExchange", mock.Anything)

	rows := listQuarantine(t, db)
	require.Len(t, rows, 1)
	require.Equal(t, bridgeQuarantineMalformedPubKey, rows[0].Reason)
}

func TestCommitReceipt_QuarantineWriteFailurePausesDrain(t *testing.T) {
	for _, tc := range []struct {
		name string
		db   func(t *testing.T) *sql.DB
	}{
		{"nil db", func(t *testing.T) *sql.DB { return nil }},
		{"closed db", func(t *testing.T) *sql.DB {
			db := openQuarantineTestDB(t)
			require.NoError(t, db.Close())
			return db
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &cosmosclient.MockCosmosMessageClient{}
			mockClient.On("GetAccountAddress").Return(quarantineTestValidator(t))

			q := NewBlockQueue(mockClient, tc.db(t))
			err := q.commitReceipt(context.Background(), quarantineTestReceipt(oversizedTransferAmount(), "0"), quarantineTestBlock("100"))
			require.Error(t, err, "a receipt is never skipped without a quarantine record")
			require.Contains(t, err.Error(), "failed to quarantine receipt")
			mockClient.AssertNotCalled(t, "BridgeExchange", mock.Anything)
		})
	}
}

// activeEpochQueryClient answers only the two queries the drain's active-validator
// pre-check makes; any other call panics on the nil embedded interface.
type activeEpochQueryClient struct {
	types.QueryClient
	validator string
}

func (c *activeEpochQueryClient) GetCurrentEpoch(context.Context, *types.QueryGetCurrentEpochRequest, ...grpc.CallOption) (*types.QueryGetCurrentEpochResponse, error) {
	return &types.QueryGetCurrentEpochResponse{Epoch: 7}, nil
}

func (c *activeEpochQueryClient) EpochGroupData(context.Context, *types.QueryGetEpochGroupDataRequest, ...grpc.CallOption) (*types.QueryGetEpochGroupDataResponse, error) {
	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: types.EpochGroupData{
		ValidationWeights: []*types.ValidationWeight{{MemberAddress: c.validator}},
	}}, nil
}

// confirmingClient stands in for the chain: it rejects what CheckTx would reject
// (ValidateBasic, surfaced as cosmosclient.BridgeExchange does) and reports every
// accepted broadcast as recorded by our validator, so confirmReceiptRecorded
// succeeds on the first poll.
type confirmingClient struct {
	*cosmosclient.MockCosmosMessageClient
	validator string
	attempts  int
	sent      []*types.MsgBridgeExchange
}

func (c *confirmingClient) BridgeExchange(msg *types.MsgBridgeExchange) error {
	c.attempts++
	if err := msg.ValidateBasic(); err != nil {
		return fmt.Errorf("bridge exchange failed: code=18 rawLog=%s", err.Error())
	}
	c.sent = append(c.sent, msg)
	return nil
}

func (c *confirmingClient) BridgeTransactionsByReceipt(_ context.Context, chain, blockNumber, receiptIndex string) ([]types.BridgeTransaction, error) {
	var out []types.BridgeTransaction
	for _, m := range c.sent {
		if m.OriginChain == chain && m.BlockNumber == blockNumber && m.ReceiptIndex == receiptIndex {
			out = append(out, types.BridgeTransaction{
				ChainId:         m.OriginChain,
				ContractAddress: m.ContractAddress,
				OwnerAddress:    m.OwnerAddress,
				Amount:          m.Amount,
				BlockNumber:     m.BlockNumber,
				ReceiptIndex:    m.ReceiptIndex,
				ReceiptsRoot:    m.ReceiptsRoot,
				Validators:      []string{c.validator},
			})
		}
	}
	return out, nil
}

func (c *confirmingClient) sentKeys() []string {
	out := make([]string, 0, len(c.sent))
	for _, m := range c.sent {
		out = append(out, m.BlockNumber+"/"+m.ReceiptIndex)
	}
	return out
}

// newDrainTestQueue returns a queue for an active validator whose cursor is
// persisted at latest, as after a previous run of this node.
func newDrainTestQueue(t *testing.T, db *sql.DB, latest uint64) (*BridgeQueue, *confirmingClient) {
	t.Helper()
	validator := quarantineTestValidator(t)
	require.NoError(t, apiconfig.SetBridgeLatestBlock(context.Background(), db, quarantineTestChain, latest))

	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return(validator)
	mockClient.On("NewInferenceQueryClient").Return(&activeEpochQueryClient{validator: validator})
	client := &confirmingClient{MockCosmosMessageClient: mockClient, validator: validator}
	return NewBlockQueue(client, db), client
}

func persistedLatest(t *testing.T, db *sql.DB) uint64 {
	t.Helper()
	latest, ok, err := apiconfig.GetBridgeLatestBlock(context.Background(), db, quarantineTestChain)
	require.NoError(t, err)
	require.True(t, ok)
	return latest
}

func TestBridgeQueue_MalformedBlockDoesNotStallCursor(t *testing.T) {
	db := openQuarantineTestDB(t)
	q, client := newDrainTestQueue(t, db, 99)

	_, err := q.AddBlock(quarantineTestBlock("100", quarantineTestReceipt(oversizedTransferAmount(), "0")))
	require.NoError(t, err)
	_, err = q.AddBlock(quarantineTestBlock("101", quarantineTestReceipt("1000", "0")))
	require.NoError(t, err)

	latest, ok := q.GetLatestBlock(quarantineTestChain)
	require.True(t, ok)
	require.Equal(t, uint64(101), latest, "cursor must move past the malformed block")
	require.Equal(t, uint64(101), persistedLatest(t, db))
	require.Equal(t, []string{"101/0"}, client.sentKeys())
	require.Equal(t, 1, client.attempts, "malformed receipt is never broadcast")
	require.Zero(t, q.GetQueueSize())

	rows := listQuarantine(t, db)
	require.Len(t, rows, 1)
	require.Equal(t, "100", rows[0].BlockNumber)
}

func TestBridgeQueue_MixedBlockSubmitsValidReceipts(t *testing.T) {
	db := openQuarantineTestDB(t)
	q, client := newDrainTestQueue(t, db, 99)

	_, err := q.AddBlock(quarantineTestBlock("100",
		quarantineTestReceipt(oversizedTransferAmount(), "0"),
		quarantineTestReceipt("1000", "1"),
		quarantineTestReceipt("0", "2"),
	))
	require.NoError(t, err)

	latest, _ := q.GetLatestBlock(quarantineTestChain)
	require.Equal(t, uint64(100), latest)
	require.Equal(t, []string{"100/1"}, client.sentKeys())
	require.Len(t, listQuarantine(t, db), 2)
}

func TestBridgeQueue_StuckNodeRecoversAndRedeliveryIsIdempotent(t *testing.T) {
	db := openQuarantineTestDB(t)

	// A node stuck on the malformed block 100 restarts with this build; the
	// feeder redelivers from latest+1.
	q, client := newDrainTestQueue(t, db, 99)
	for n := 105; n >= 100; n-- {
		receipts := []BridgeReceipt{quarantineTestReceipt("1000", "0")}
		if n == 100 {
			receipts = []BridgeReceipt{quarantineTestReceipt(oversizedTransferAmount(), "0")}
		}
		_, err := q.AddBlock(quarantineTestBlock(strconv.Itoa(n), receipts...))
		require.NoError(t, err)
	}
	latest, _ := q.GetLatestBlock(quarantineTestChain)
	require.Equal(t, uint64(105), latest)
	require.Equal(t, []string{"101/0", "102/0", "103/0", "104/0", "105/0"}, client.sentKeys())

	// Restart again and redeliver the malformed block: it is a duplicate now.
	q2, client2 := newDrainTestQueue(t, db, 105)
	_, err := q2.AddBlock(quarantineTestBlock("100", quarantineTestReceipt(oversizedTransferAmount(), "0")))
	require.NoError(t, err)
	require.Empty(t, client2.sentKeys())
	require.Len(t, listQuarantine(t, db), 1)

	// Quarantining the same receipt twice keeps one record.
	require.NoError(t, q2.quarantineReceipt(context.Background(),
		quarantineTestReceipt(oversizedTransferAmount(), "0"), quarantineTestBlock("100"),
		bridgeQuarantineMalformedContent, errors.New("again")))
	rows := listQuarantine(t, db)
	require.Len(t, rows, 1)
	require.Contains(t, rows[0].Error, "amount exceeds 78 digits", "first record wins")
}

func TestInsertBridgeQuarantinedReceiptReportsDuplicate(t *testing.T) {
	db := openQuarantineTestDB(t)
	r := apiconfig.BridgeQuarantinedReceipt{
		ChainId: quarantineTestChain, BlockNumber: "100", ReceiptIndex: "0",
		Reason: bridgeQuarantineMalformedContent, Error: "first",
	}
	inserted, err := apiconfig.InsertBridgeQuarantinedReceipt(context.Background(), db, r, "1")
	require.NoError(t, err)
	require.True(t, inserted)

	r.Error = "second"
	inserted, err = apiconfig.InsertBridgeQuarantinedReceipt(context.Background(), db, r, "1")
	require.NoError(t, err)
	require.False(t, inserted, "re-quarantining the same receipt must not count as a new record")
}

func getBridgeStatusForTest(t *testing.T, q *BridgeQueue) map[string]json.RawMessage {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/v1/bridge/status", nil), rec)
	require.NoError(t, (&Server{blockQueue: q}).getBridgeStatus(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestGetBridgeStatusQuarantinedCount(t *testing.T) {
	db := openQuarantineTestDB(t)
	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return(quarantineTestValidator(t))
	q := NewBlockQueue(mockClient, db)

	require.JSONEq(t, "0", string(getBridgeStatusForTest(t, q)["quarantinedCount"]))

	require.NoError(t, q.commitReceipt(context.Background(), quarantineTestReceipt(oversizedTransferAmount(), "0"), quarantineTestBlock("100")))
	require.JSONEq(t, "1", string(getBridgeStatusForTest(t, q)["quarantinedCount"]))

	// An unreadable store must not look like a clean queue.
	require.NoError(t, db.Close())
	require.JSONEq(t, "null", string(getBridgeStatusForTest(t, q)["quarantinedCount"]))
}

func TestBridgeQueue_InactiveValidatorSkipsMalformedWithoutQuarantine(t *testing.T) {
	db := openQuarantineTestDB(t)
	validator := quarantineTestValidator(t)
	require.NoError(t, apiconfig.SetBridgeLatestBlock(context.Background(), db, quarantineTestChain, 99))

	mockClient := &cosmosclient.MockCosmosMessageClient{}
	mockClient.On("GetAccountAddress").Return(validator)
	mockClient.On("NewInferenceQueryClient").Return(&activeEpochQueryClient{validator: "someone-else"})
	client := &confirmingClient{MockCosmosMessageClient: mockClient, validator: validator}
	q := NewBlockQueue(client, db)

	_, err := q.AddBlock(quarantineTestBlock("100", quarantineTestReceipt(oversizedTransferAmount(), "0")))
	require.NoError(t, err)
	_, err = q.AddBlock(quarantineTestBlock("101"))
	require.NoError(t, err)

	latest, _ := q.GetLatestBlock(quarantineTestChain)
	require.Equal(t, uint64(101), latest)
	require.Zero(t, client.attempts)
	require.Empty(t, listQuarantine(t, db), "inactive validators skip receipts before the pre-check")
}
